import { test as base } from "@playwright/test";

import { Fixture, readFixture } from "./fixture";

/**
 * test carries the bootstrap's fixture. It is read per worker rather than at
 * import time because Playwright collects the spec files before the global
 * setup that writes it has run.
 */
export const test = base.extend<{ fixture: Fixture }>({
  fixture: async ({}, use) => {
    await use(readFixture());
  },
});

export { expect } from "@playwright/test";
