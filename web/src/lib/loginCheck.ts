/**
 * The browser check the login page solves after a failed sign-in.
 *
 * The server hands out a puzzle; the browser finds a nonce whose SHA-256 over
 * "<puzzle>:<nonce>" starts with `difficulty` zero bits, and sends the nonce
 * back with the password. The hash is written here in plain TypeScript because
 * crypto.subtle exists only in secure contexts, and the panel is often reached
 * over plain HTTP on a LAN address.
 */

const K = Uint32Array.from([
  1116352408, 1899447441, 3049323471, 3921009573, 961987163, 1508970993, 2453635748, 2870763221,
  3624381080, 310598401, 607225278, 1426881987, 1925078388, 2162078206, 2614888103, 3248222580,
  3835390401, 4022224774, 264347078, 604807628, 770255983, 1249150122, 1555081692, 1996064986,
  2554220882, 2821834349, 2952996808, 3210313671, 3336571891, 3584528711, 113926993, 338241895,
  666307205, 773529912, 1294757372, 1396182291, 1695183700, 1986661051, 2177026350, 2456956037,
  2730485921, 2820302411, 3259730800, 3345764771, 3516065817, 3600352804, 4094571909, 275423344,
  430227734, 506948616, 659060556, 883997877, 958139571, 1322822218, 1537002063, 1747873779,
  1955562222, 2024104815, 2227730452, 2361852424, 2428436474, 2756734187, 3204031479, 3329325298,
])

const H0 = Uint32Array.from([
  1779033703, 3144134277, 1013904242, 2773480762, 1359893119, 2600822924, 528734635, 1541459225,
])

function rotr(x: number, n: number): number {
  return (x >>> n) | (x << (32 - n))
}

/** SHA-256 of `message`, as 32 bytes. */
export function sha256(message: Uint8Array): Uint8Array {
  const bitLength = message.length * 8
  // The message, a 0x80 byte, zero padding, then the 64-bit length: whole blocks.
  const total = Math.ceil((message.length + 9) / 64) * 64
  const block = new Uint8Array(total)
  block.set(message)
  block[message.length] = 0x80
  const view = new DataView(block.buffer)
  view.setUint32(total - 8, Math.floor(bitLength / 0x100000000))
  view.setUint32(total - 4, bitLength >>> 0)

  const H = Uint32Array.from(H0)
  const W = new Uint32Array(64)
  for (let offset = 0; offset < total; offset += 64) {
    for (let t = 0; t < 16; t++) W[t] = view.getUint32(offset + t * 4)
    for (let t = 16; t < 64; t++) {
      const x = W[t - 15]
      const y = W[t - 2]
      const s0 = rotr(x, 7) ^ rotr(x, 18) ^ (x >>> 3)
      const s1 = rotr(y, 17) ^ rotr(y, 19) ^ (y >>> 10)
      W[t] = W[t - 16] + s0 + W[t - 7] + s1
    }

    let a = H[0]
    let b = H[1]
    let c = H[2]
    let d = H[3]
    let e = H[4]
    let f = H[5]
    let g = H[6]
    let h = H[7]
    for (let t = 0; t < 64; t++) {
      const S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)
      const ch = (e & f) ^ (~e & g)
      const t1 = (h + S1 + ch + K[t] + W[t]) | 0
      const S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)
      const maj = (a & b) ^ (a & c) ^ (b & c)
      const t2 = (S0 + maj) | 0
      h = g
      g = f
      f = e
      e = (d + t1) | 0
      d = c
      c = b
      b = a
      a = (t1 + t2) | 0
    }
    H[0] = H[0] + a
    H[1] = H[1] + b
    H[2] = H[2] + c
    H[3] = H[3] + d
    H[4] = H[4] + e
    H[5] = H[5] + f
    H[6] = H[6] + g
    H[7] = H[7] + h
  }

  const out = new Uint8Array(32)
  const outView = new DataView(out.buffer)
  for (let i = 0; i < 8; i++) outView.setUint32(i * 4, H[i])
  return out
}

/** The number of zero bits at the start of a digest. */
export function leadingZeroBits(digest: Uint8Array): number {
  let n = 0
  for (const byte of digest) {
    if (byte === 0) {
      n += 8
      continue
    }
    return n + Math.clz32(byte) - 24
  }
  return n
}

/** What the login page sends back for a solved check. */
export interface LoginCheckAnswer {
  challenge: string
  nonce: string
}

/** Thrown when the visitor unticks the box before the answer is found. */
export class LoginCheckCancelled extends Error {
  constructor() {
    super("login check cancelled")
    this.name = "LoginCheckCancelled"
  }
}

const TRIES_PER_TICK = 400

/**
 * Finds a nonce for `challenge` with `difficulty` leading zero bits. It yields
 * to the event loop between batches, so the page stays responsive and the
 * progress callback runs. It stops with LoginCheckCancelled when `signal` aborts.
 */
export async function solveLoginCheck(
  challenge: string,
  difficulty: number,
  options: { signal: AbortSignal; onProgress?: (percent: number) => void },
): Promise<string> {
  const encoder = new TextEncoder()
  const prefix = encoder.encode(challenge + ":")
  // About 2^difficulty tries are expected; the bar follows 1 - e^(-tries/2^d), so
  // it climbs quickly and never claims completion before the answer exists.
  const expected = Math.pow(2, Math.min(difficulty, 52))
  let nonce = 0

  for (;;) {
    if (options.signal.aborted) throw new LoginCheckCancelled()
    for (let i = 0; i < TRIES_PER_TICK; i++, nonce++) {
      const digits = encoder.encode(String(nonce))
      const message = new Uint8Array(prefix.length + digits.length)
      message.set(prefix)
      message.set(digits, prefix.length)
      if (leadingZeroBits(sha256(message)) >= difficulty) return String(nonce)
    }
    options.onProgress?.(Math.min(99, Math.floor(100 * (1 - Math.exp(-nonce / expected)))))
    await new Promise<void>((resolve) => setTimeout(resolve, 0))
  }
}
