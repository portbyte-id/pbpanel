import { describe, expect, it } from "vitest"

import { ApiError } from "@/lib/api"
import { describeError } from "@/lib/errors"

describe("describeError", () => {
  it("returns the server's detail for an ApiError", () => {
    const err = new ApiError(409, "conflict", "another app already uses that port")
    expect(describeError(err, "The operation failed.")).toBe("another app already uses that port")
  })

  it("appends the validator output when the server sent it", () => {
    const err = new ApiError(422, "config_rejected", "nginx rejected the configuration", {
      output: 'nginx: [emerg] unknown directive "foo"\n',
    })
    expect(describeError(err, "The change failed.")).toBe(
      'nginx rejected the configuration\n\nnginx: [emerg] unknown directive "foo"',
    )
  })

  it("uses the fallback for a value that is not an ApiError", () => {
    expect(describeError(new Error("network down"), "The panel could not be reached.")).toBe(
      "The panel could not be reached.",
    )
    expect(describeError(null, "The panel could not be reached.")).toBe(
      "The panel could not be reached.",
    )
  })

  it("ignores blank output", () => {
    const err = new ApiError(500, "apply_failed", "the change failed", { output: "  \n" })
    expect(describeError(err, "x")).toBe("the change failed")
  })
})
