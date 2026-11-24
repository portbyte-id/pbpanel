import { describe, expect, it } from "vitest"

import { parseReleaseNotes } from "@/lib/release-notes"
import {
  formatBytes,
  isUpgradeActive,
  upgradePercent,
  upgradeStepIndex,
} from "@/lib/update"

describe("upgrade phases", () => {
  it("treats only unfinished phases as active, so polling stops at the end", () => {
    for (const phase of ["queued", "checking", "downloading", "installing", "restarting", "health"] as const) {
      expect(isUpgradeActive(phase)).toBe(true)
    }
    for (const phase of ["idle", "done", "up_to_date", "rolled_back", "failed", "interrupted"] as const) {
      expect(isUpgradeActive(phase)).toBe(false)
    }
    expect(isUpgradeActive(undefined)).toBe(false)
  })

  it("orders the stepper the way an upgrade runs", () => {
    expect(upgradeStepIndex("checking")).toBeLessThan(upgradeStepIndex("downloading"))
    expect(upgradeStepIndex("downloading")).toBeLessThan(upgradeStepIndex("installing"))
    expect(upgradeStepIndex("restarting")).toBeLessThan(upgradeStepIndex("health"))
    expect(upgradeStepIndex("idle")).toBe(-1)
  })

  it("reports byte progress only while downloading", () => {
    expect(upgradePercent({ phase: "downloading", done: 50, total: 200 })).toBe(25)
    expect(upgradePercent({ phase: "downloading", done: 300, total: 200 })).toBe(100)
    expect(upgradePercent({ phase: "downloading", done: 0, total: 0 })).toBeNull()
    expect(upgradePercent({ phase: "installing", done: 0, total: 0 })).toBeNull()
  })

  it("formats byte counts for people", () => {
    expect(formatBytes(512)).toBe("512 B")
    expect(formatBytes(2048)).toBe("2.0 KB")
    expect(formatBytes(4_404_019)).toBe("4.2 MB")
    expect(formatBytes(20 * 1024 * 1024)).toBe("20 MB")
  })
})

describe("release notes", () => {
  it("parses headings, bullets and paragraphs", () => {
    const blocks = parseReleaseNotes(
      ["## Changes", "- faster start", "* fixed the nginx reload", "", "Thanks to everyone.", "continued"].join("\n"),
    )
    expect(blocks).toEqual([
      { kind: "heading", level: 2, text: "Changes" },
      { kind: "list", items: ["faster start", "fixed the nginx reload"] },
      { kind: "paragraph", text: "Thanks to everyone. continued" },
    ])
  })

  it("keeps markup as plain text rather than parsing it", () => {
    const blocks = parseReleaseNotes("<script>alert(1)</script>")
    expect(blocks).toEqual([{ kind: "paragraph", text: "<script>alert(1)</script>" }])
  })

  it("returns nothing for empty notes", () => {
    expect(parseReleaseNotes("  \n\n")).toEqual([])
  })
})
