import { execFileSync } from "node:child_process";

// The suite's own database, created by run.sh. A few scenarios have to move
// time — a booked interview is two hours out, and a room opens ten minutes
// before it — so they shift rows the way a passing afternoon would, through
// the Compose Postgres the stack runs against.
const database = process.env.E2E_DATABASE_NAME ?? "recruiting_e2e";
const container = process.env.E2E_POSTGRES_CONTAINER ?? "recruiting-postgres-1";

/** sql runs one statement as the schema owner and returns psql's output. */
export function sql(statement: string): string {
  return execFileSync(
    "docker",
    ["exec", "-i", container, "psql", "-q", "-t", "-A", "-U", "recruiting", "-d", database, "-v", "ON_ERROR_STOP=1", "-c", statement],
    { encoding: "utf8" },
  ).trim();
}
