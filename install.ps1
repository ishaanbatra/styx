# Install the latest Windows styx release without prompting.
# STYX_DOWNLOAD_BASE is intentionally undocumented; it exists for installer tests.

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Get-EffectiveUri {
    param([Parameter(Mandatory = $true)] $Response)

    $baseResponse = $Response.BaseResponse
    if ($null -ne $baseResponse -and $null -ne $baseResponse.PSObject.Properties["ResponseUri"]) {
        return $baseResponse.ResponseUri.AbsoluteUri
    }
    if ($null -ne $baseResponse -and $null -ne $baseResponse.PSObject.Properties["RequestMessage"]) {
        return $baseResponse.RequestMessage.RequestUri.AbsoluteUri
    }
    return [string]$Response.BaseResponse
}

$downloadBase = if ([string]::IsNullOrWhiteSpace($env:STYX_DOWNLOAD_BASE)) {
    "https://github.com/ishaanbatra/styx"
} else {
    $env:STYX_DOWNLOAD_BASE.TrimEnd("/")
}

$machineArch = try {
    [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
} catch {
    if (-not [string]::IsNullOrWhiteSpace($env:PROCESSOR_ARCHITEW6432)) {
        $env:PROCESSOR_ARCHITEW6432
    } else {
        $env:PROCESSOR_ARCHITECTURE
    }
}

switch ($machineArch.ToLowerInvariant()) {
    { $_ -in @("x64", "amd64") } { $arch = "amd64"; break }
    { $_ -in @("arm64", "aarch64") } { $arch = "arm64"; break }
    default { throw "Unsupported Windows architecture '$machineArch'." }
}

Write-Host "Resolving latest version..."
$version = $null
try {
    $latestResponse = Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/releases/latest/download/latest.json"
    $latest = $latestResponse.Content | ConvertFrom-Json
    $version = [string]$latest.version
} catch {
    $version = $null
}

if ([string]::IsNullOrWhiteSpace($version)) {
    try {
        $latestResponse = Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/releases/latest"
        $effectiveUri = Get-EffectiveUri -Response $latestResponse
        if ($effectiveUri -match "/tag/([^/?#]+)") {
            $version = $Matches[1]
        }
    } catch {
        throw "Could not resolve the latest styx release version: $($_.Exception.Message)"
    }
}

if ([string]::IsNullOrWhiteSpace($version) -or $version -notmatch '^v?[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z][0-9A-Za-z.-]*)?$') {
    throw "Could not resolve a valid latest styx release version."
}

$artifactVersion = if ($version.StartsWith("v", [System.StringComparison]::OrdinalIgnoreCase)) {
    $version.Substring(1)
} else {
    $version
}
$archiveName = "styx_${artifactVersion}_windows_${arch}.zip"
$releaseBase = "$downloadBase/releases/download/$version"
$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("styx-install-" + [System.Guid]::NewGuid().ToString("N"))
$installTemp = $null

New-Item -ItemType Directory -Path $tempDir | Out-Null
try {
    $archivePath = Join-Path $tempDir $archiveName
    $checksumsPath = Join-Path $tempDir "checksums.txt"
    $extractDir = Join-Path $tempDir "extract"

    Write-Host "Installing styx $version (windows/$arch)..."
    Invoke-WebRequest -UseBasicParsing -Uri "$releaseBase/$archiveName" -OutFile $archivePath
    Invoke-WebRequest -UseBasicParsing -Uri "$releaseBase/checksums.txt" -OutFile $checksumsPath

    $expectedHashes = @(
        Get-Content -LiteralPath $checksumsPath | ForEach-Object {
            if ($_ -match '^\s*([0-9A-Fa-f]{64})\s+\*?(.+?)\s*$') {
                $candidateHash = $Matches[1]
                $candidateName = $Matches[2]
                if ($candidateName -ceq $archiveName) {
                    $candidateHash
                }
            }
        }
    )
    if ($expectedHashes.Count -ne 1) {
        throw "Expected exactly one checksum entry for $archiveName."
    }

    $actualHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash
    if (-not [string]::Equals($expectedHashes[0], $actualHash, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Checksum mismatch for $archiveName (expected $($expectedHashes[0]), got $actualHash)."
    }
    Write-Host "Checksum verified."

    New-Item -ItemType Directory -Path $extractDir | Out-Null
    Expand-Archive -LiteralPath $archivePath -DestinationPath $extractDir -Force
    $sourceBinary = Join-Path $extractDir "styx.exe"
    if (-not (Test-Path -LiteralPath $sourceBinary -PathType Leaf)) {
        throw "Extracted styx.exe was not found."
    }

    if ([string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        throw "LOCALAPPDATA is not set."
    }
    $installDir = Join-Path $env:LOCALAPPDATA "styx\bin"
    $targetBinary = Join-Path $installDir "styx.exe"
    $backupBinary = "$targetBinary.old.bak"
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null

    $installTemp = Join-Path $installDir (".styx." + [System.Guid]::NewGuid().ToString("N") + ".tmp")
    Copy-Item -LiteralPath $sourceBinary -Destination $installTemp
    if (Test-Path -LiteralPath $targetBinary -PathType Leaf) {
        Copy-Item -LiteralPath $targetBinary -Destination $backupBinary -Force
        Write-Host "Backed up existing styx -> $backupBinary"
        [System.IO.File]::Replace($installTemp, $targetBinary, $null)
    } else {
        [System.IO.File]::Move($installTemp, $targetBinary)
    }
    $installTemp = $null
    Write-Host "Installed -> $targetBinary"

    $userPath = [System.Environment]::GetEnvironmentVariable("Path", "User")
    $normalizedInstallDir = $installDir.TrimEnd("\")
    $pathContainsInstallDir = @(
        ([string]$userPath -split ";") | Where-Object {
            -not [string]::IsNullOrWhiteSpace($_) -and
            [string]::Equals(
                [System.Environment]::ExpandEnvironmentVariables($_).Trim().TrimEnd("\"),
                $normalizedInstallDir,
                [System.StringComparison]::OrdinalIgnoreCase
            )
        }
    ).Count -gt 0

    if (-not $pathContainsInstallDir) {
        $newUserPath = if ([string]::IsNullOrWhiteSpace($userPath)) {
            $installDir
        } else {
            "$($userPath.TrimEnd(';'));$installDir"
        }
        [System.Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
        $env:Path = "$installDir;$env:Path"
        Write-Host "Added $installDir to your user PATH. Open a new terminal to use styx."
    }
} finally {
    if (-not [string]::IsNullOrWhiteSpace($installTemp) -and (Test-Path -LiteralPath $installTemp)) {
        Remove-Item -LiteralPath $installTemp -Force
    }
    if (Test-Path -LiteralPath $tempDir) {
        Remove-Item -LiteralPath $tempDir -Recurse -Force
    }
}
