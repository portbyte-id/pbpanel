import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { RangePicker } from "@/components/RangePicker"

describe("RangePicker", () => {
  it("is a named radio group of the six ranges with the current one checked", () => {
    render(<RangePicker value="1h" onChange={() => {}} />)
    const group = screen.getByRole("radiogroup", { name: "History range" })
    expect(group).toBeInTheDocument()
    expect(screen.getAllByRole("radio").map((r) => r.getAttribute("aria-label"))).toEqual([
      "1 hour",
      "6 hours",
      "24 hours",
      "7 days",
      "30 days",
      "90 days",
    ])
    expect(screen.getByRole("radio", { name: "1 hour" })).toBeChecked()
    expect(screen.getByRole("radio", { name: "6 hours" })).not.toBeChecked()
  })

  it("reports a pick, and the arrow keys move between ranges", async () => {
    const onChange = vi.fn()
    const user = userEvent.setup()
    render(<RangePicker value="1h" onChange={onChange} />)
    await user.click(screen.getByRole("radio", { name: "7 days" }))
    expect(onChange).toHaveBeenLastCalledWith("7d")

    screen.getByRole("radio", { name: "1 hour" }).focus()
    await user.keyboard("{ArrowRight}")
    expect(onChange).toHaveBeenLastCalledWith("6h")
  })
})
