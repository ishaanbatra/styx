// Custom promptfoo provider that byte-matches internal/brain/brain.go's
// chat(): POST /api/chat with stream:false, think:false, format:ActionSchema,
// options.temperature:0, and a [system,user] message pair. keep_alive keeps the
// model warm across the run. We use a custom provider (not promptfoo's native
// ollama:chat) because that exact body — think:false together with a structured
// `format` schema — is what real styx sends, and fidelity to brain.go is the
// whole point: an eval that diverges from reality is worse than no eval.
const fs = require('fs');
const path = require('path');

const SCHEMA = JSON.parse(
  fs.readFileSync(path.join(__dirname, 'generated', 'schema.json'), 'utf8'),
);
const BASE_URL = process.env.OLLAMA_BASE_URL || 'http://localhost:11434';
const MODEL = process.env.STYX_BRAIN_MODEL || 'qwen2.5-coder:7b';

async function callOnce(messages) {
  const body = {
    model: MODEL,
    stream: false,
    think: false,
    format: SCHEMA,
    keep_alive: '10m',
    options: { temperature: 0 },
    messages,
  };
  const res = await fetch(BASE_URL + '/api/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`ollama ${res.status}: ${await res.text()}`);
  }
  const j = await res.json();
  return (j.message && j.message.content) || '';
}

// promptfoo loads a file:// provider via `new (require(path))(options)`, so the
// module must export a constructor/class implementing ApiProvider.
class StyxBrainProvider {
  constructor(options) {
    this.providerId = (options && options.id) || `styx-brain:${MODEL}`;
  }

  id() {
    return this.providerId;
  }

  // prompt is the JSON-stringified [system,user] message array from prompt.js.
  async callApi(prompt) {
    let messages;
    try {
      messages = JSON.parse(prompt);
    } catch (e) {
      return { error: `prompt not JSON messages: ${e.message}` };
    }
    // Mirror Ollama.Decide's loop: up to 2 attempts, retry once on empty or
    // unparseable content (the local model occasionally returns empty under load).
    let content = '';
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        content = await callOnce(messages);
      } catch (e) {
        content = '';
        continue;
      }
      if (!content) continue;
      try {
        JSON.parse(content);
        return { output: content };
      } catch (_) {
        // invalid JSON; retry once like Decide
      }
    }
    // Return whatever we have (possibly empty/invalid); the gate assertion will
    // score it as a miss, exactly as the Go gate counts an errored decision.
    return { output: content };
  }
}

module.exports = StyxBrainProvider;
