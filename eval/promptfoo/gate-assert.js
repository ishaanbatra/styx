// promptfoo javascript-assertion wrapper. Reads the want_* labels from the
// test vars (set by gen-tests.js) and applies the shared gate logic.
const { check } = require('./gate.js');

module.exports = (output, context) => {
  const v = (context && context.vars) || {};
  const r = check(output, {
    want_action: v.want_action,
    want_thread: v.want_thread,
    want_pipeline: v.want_pipeline,
  });
  return { pass: r.pass, score: r.pass ? 1 : 0, reason: r.reason };
};
