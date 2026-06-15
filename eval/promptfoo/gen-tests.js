// gen-tests.js generates one promptfoo test per fixture in
// testdata/brain/utterances.json — the SAME dataset the Go gate uses, never a
// fork. Each test carries the want_* labels in vars and asserts via gate.js so
// promptfoo and TestRoutingAccuracy can never disagree on what "correct" means.
const fs = require('fs');
const path = require('path');

module.exports = async function () {
  const file = path.join(__dirname, '..', '..', 'testdata', 'brain', 'utterances.json');
  const cases = JSON.parse(fs.readFileSync(file, 'utf8'));
  return cases.map((c, i) => ({
    description: `#${i + 1}: ${c.utterance}`,
    vars: {
      utterance: c.utterance,
      want_action: c.want_action || '',
      want_thread: c.want_thread || '',
      want_pipeline: c.want_pipeline || '',
    },
    assert: [{ type: 'javascript', value: 'file://gate-assert.js' }],
  }));
};
