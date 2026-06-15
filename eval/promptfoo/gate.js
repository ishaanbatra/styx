// gate.js replicates internal/brain/action.go's Action.Valid() and
// integration_test.go's TestRoutingAccuracy match logic, so a promptfoo "pass"
// means exactly what a Go-gate "correct" means. Single source of truth: the
// labels live only in testdata/brain/utterances.json (see gen-tests.js).
const VALID_THREADS = { claude: 1, codex: 1, agy: 1, ollama: 1 };
const VALID_PIPELINES = { research: 1, auto: 1, review: 1, intel: 1 };

// valid mirrors Action.Valid (action.go): structural usability the REPL checks
// before trusting a response. Decide() retries/errors on invalid actions, so an
// invalid action is effectively a gate MISS.
function valid(a) {
  switch (a.action) {
    case 'reply':
      return typeof a.reply === 'string' && a.reply !== '';
    case 'dispatch':
    case 'parallel_dispatch':
      if (!Array.isArray(a.dispatches) || a.dispatches.length === 0) return false;
      return a.dispatches.every(
        (d) => VALID_THREADS[d.thread] && typeof d.message === 'string' && d.message !== '',
      );
    case 'pipeline':
      return !!VALID_PIPELINES[a.pipeline];
    case 'remember':
      return typeof a.remember === 'string' && a.remember !== '';
    case 'handoff':
    case 'escalate':
      return true;
    default:
      return false;
  }
}

function describe(a) {
  let s = a.action;
  if (a.dispatches && a.dispatches[0]) s += '/' + a.dispatches[0].thread;
  if (a.pipeline) s += '/' + a.pipeline;
  return s;
}

// check applies the integration_test.go match logic: action must equal
// want_action; if want_thread set, dispatches[0].thread must match; if
// want_pipeline set, pipeline must match.
function check(output, want) {
  let a;
  try {
    a = JSON.parse(output);
  } catch (e) {
    return { pass: false, reason: 'invalid JSON: ' + (output ? String(output).slice(0, 80) : 'empty') };
  }
  if (!valid(a)) return { pass: false, reason: 'structurally invalid: ' + JSON.stringify(a).slice(0, 80) };

  let ok = a.action === want.want_action;
  if (ok && want.want_thread) {
    ok = Array.isArray(a.dispatches) && a.dispatches.length > 0 && a.dispatches[0].thread === want.want_thread;
  }
  if (ok && want.want_pipeline) {
    ok = a.pipeline === want.want_pipeline;
  }

  const wantStr =
    want.want_action +
    (want.want_thread ? '/' + want.want_thread : '') +
    (want.want_pipeline ? '/' + want.want_pipeline : '');
  return { pass: ok, reason: ok ? 'ok' : `got ${describe(a)} want ${wantStr}` };
}

module.exports = { valid, check };
