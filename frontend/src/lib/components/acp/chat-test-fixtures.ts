// Synthetic Forge identities for component fixtures. Protocol behavior tests
// specify their identities and versions explicitly in chat-session.test.ts.
export function fixtureMessages(messages: readonly Record<string, unknown>[], offset = 0) {
  return messages.map((message, index) => ({
    id: `entry-${offset + index}`,
    previousId: offset + index === 0 ? "" : `entry-${offset + index - 1}`,
    version: 1,
    ...message,
  }));
}

export function fixtureState(state: Record<string, unknown>) {
  const messages = fixtureMessages(
    (state.messages ?? []) as Record<string, unknown>[],
    Number(state.messageOffset ?? 0),
  );
  return { messageCursor: 1, ...state, messages, lastMessageId: messages.at(-1)?.id ?? "" };
}
