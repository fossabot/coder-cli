#!/usr/bin/env -S deno run --allow-all
import { root, execInherit, requireNoFilesChanged, isCI } from "./lib.ts"

await root()

console.log("--- formatting")
await execInherit("go mod tidy")
await execInherit("gofmt -w -s .")
await execInherit(`goimports -w "-local=$$(go list -m)" .`)

if (isCI()) {
  await requireNoFilesChanged()
}