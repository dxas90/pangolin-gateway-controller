# Voice Mode Interaction

## Rule: Always respond with TEXT after voice input

- Voice input does NOT mean the user wants voice output
- After receiving voice input, respond as **regular text** — do NOT use `mcp__voicemode__converse` for responses
- Only use voicemode tools for the initial listen/acknowledgment
- Exception: voice output OK if user explicitly asks to "speak your response"

## Listening Mode

When user types "l" or "listen": start listening, then respond with text.
