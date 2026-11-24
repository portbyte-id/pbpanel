import { createHash } from "node:crypto"
import { describe, expect, it } from "vitest"

import { LoginCheckCancelled, leadingZeroBits, sha256, solveLoginCheck } from "@/lib/loginCheck"

const encode = (s: string) => new TextEncoder().encode(s)

describe("sha256", () => {
  it("matches node's SHA-256 across the padding boundaries", () => {
    for (const len of [0, 1, 55, 56, 63, 64, 65, 119, 120, 127, 128, 1000]) {
      const text = "x".repeat(len)
      const want = createHash("sha256").update(text).digest("hex")
      const got = Buffer.from(sha256(encode(text))).toString("hex")
      expect(got, `length ${len}`).toBe(want)
    }
  })

  it("matches node on multi-byte UTF-8", () => {
    const text = "puzzle:12345 — ünïcødé ✓"
    expect(Buffer.from(sha256(encode(text))).toString("hex")).toBe(
      createHash("sha256").update(text).digest("hex"),
    )
  })
})

describe("leadingZeroBits", () => {
  it("counts zero bits from the first byte", () => {
    expect(leadingZeroBits(Uint8Array.of(0x80, 0))).toBe(0)
    expect(leadingZeroBits(Uint8Array.of(0x01, 0))).toBe(7)
    expect(leadingZeroBits(Uint8Array.of(0x00, 0x80))).toBe(8)
    expect(leadingZeroBits(Uint8Array.of(0x00, 0x01))).toBe(15)
    expect(leadingZeroBits(new Uint8Array(32))).toBe(256)
  })
})

describe("solveLoginCheck", () => {
  it("finds a nonce whose answer has the difficulty's zero bits", async () => {
    const challenge = "1700000000.abcdef.10.0123456789abcdef"
    const nonce = await solveLoginCheck(challenge, 10, { signal: new AbortController().signal })

    const digest = createHash("sha256").update(`${challenge}:${nonce}`).digest()
    expect(leadingZeroBits(new Uint8Array(digest))).toBeGreaterThanOrEqual(10)
  })

  it("reports progress within 0..99 while it works", async () => {
    const seen: number[] = []
    await solveLoginCheck("progress-check", 12, {
      signal: new AbortController().signal,
      onProgress: (p) => seen.push(p),
    })
    for (const p of seen) {
      expect(p).toBeGreaterThanOrEqual(0)
      expect(p).toBeLessThanOrEqual(99)
    }
  })

  it("stops when the visitor cancels", async () => {
    const abort = new AbortController()
    abort.abort()
    await expect(solveLoginCheck("never-solved", 40, { signal: abort.signal })).rejects.toBeInstanceOf(
      LoginCheckCancelled,
    )
  })
})
