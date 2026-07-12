// Package attribution defines the identity styx stamps onto work that
// lands in git. Styx never runs `git commit` itself — the agents it
// dispatches do — so commit attribution is an instruction embedded in
// write-capable agent prompts, plus a footer styx appends to PR bodies
// it creates. The email belongs to the styx-thetrickster GitHub App's
// bot user (ID 302670164), so GitHub renders the app's avatar on
// commits and in the Contributors sidebar.
package attribution

// Trailer is the exact Co-Authored-By line agents must end every commit
// message with.
const Trailer = "Co-Authored-By: styx-thetrickster[bot] <302670164+styx-thetrickster[bot]@users.noreply.github.com>"

// CommitInstruction is the sentence write-capable agent prompts embed so
// every commit carries Trailer.
const CommitInstruction = "End every git commit message with this exact trailer line, verbatim, on its own line at the very end: " + Trailer

// PRFooter is appended as its own final paragraph to every PR body styx
// creates.
const PRFooter = "Generated with [styx](https://github.com/ishaanbatra/styx)"
