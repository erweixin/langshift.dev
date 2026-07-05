// Package agentcore contains the cloud-agent execution kernel.
//
// The kernel owns the reusable execution mechanics: claim an agent job,
// allocate an attempt key, call the LLM client, append terminal run events with
// the current job fence, and fail or acknowledge leased jobs according to the
// durable append result. Business packages provide handlers that know how to
// parse their payloads, build prompts, validate structured output, and persist
// domain facts.
package agentcore
