// Helpers every real-board adapter shares.

export function requireEnv(env, name) {
  const v = env[name];
  if (!v) throw new Error(`${name} is not set; the board needs an account to post as`);
  return v;
}

// expectVisible waits for an element and names it when it never shows, so a
// board that changed its page fails with "could not find X" and not with a
// timeout on an anonymous selector.
export async function expectVisible(page, locator, what) {
  try {
    await locator.first().waitFor({ state: "visible" });
  } catch {
    throw new Error(`could not find ${what} on ${page.url()}; the board's page may have changed`);
  }
}
