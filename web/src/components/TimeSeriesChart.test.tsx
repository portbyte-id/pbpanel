import { fireEvent, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"

import { TimeSeriesChart } from "@/components/TimeSeriesChart"
import { fill, times } from "@/test/metricsFixtures"

const pct = (v: number) => `${v}%`

function chart(values: (number | null)[], extra: Partial<React.ComponentProps<typeof TimeSeriesChart>> = {}) {
  return (
    <TimeSeriesChart
      title="CPU"
      t={times(values.length)}
      step={60}
      format={pct}
      series={[{ key: "cpu", label: "CPU average", values }]}
      {...extra}
    />
  )
}

describe("TimeSeriesChart", () => {
  it("breaks the line at a null instead of drawing across the gap", () => {
    const { container } = render(chart([10, 20, null, 40, 50]))
    const d = container.querySelector('path[data-series="cpu"]')?.getAttribute("d") ?? ""
    expect(d.match(/M/g)).toHaveLength(2)
    // Five points are 120 viewBox units apart from x=56: index 2 sits at x=236 and is skipped.
    expect(d).not.toContain("236.0,")
  })

  it("draws a series of one point as a dot, not as nothing", () => {
    const { container } = render(chart([null, 5, null]))
    expect(container.querySelector('path[data-series="cpu"]')?.getAttribute("d")).toMatch(/h0$/)
  })

  it("names the chart, its legend and its data table", async () => {
    const user = userEvent.setup()
    render(
      chart([1, 2, 3], {
        series: [
          { key: "a", label: "CPU average", values: [1, 2, 3] },
          { key: "b", label: "CPU peak", values: [2, 3, 4], dash: "6 3" },
        ],
      }),
    )
    expect(screen.getByRole("figure", { name: "CPU" })).toBeInTheDocument()
    expect(screen.getByRole("img", { name: /^CPU: 2 series, 3 points/ })).toBeInTheDocument()
    const legend = screen.getByRole("list", { name: "CPU legend" })
    expect(within(legend).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["CPU average", "CPU peak"])

    await user.click(screen.getByText("Show data"))
    const table = screen.getByRole("table", { name: /^CPU, one row per 1 minute, newest first/ })
    expect(within(table).getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Time", "CPU average", "CPU peak"])
    // Newest first: the last point (3 and 4) is the first body row.
    const firstRow = within(table).getAllByRole("row")[1]
    expect(firstRow).toHaveTextContent("3%")
    expect(firstRow).toHaveTextContent("4%")
  })

  it("shows a dash for a missing point in the table", async () => {
    const user = userEvent.setup()
    render(chart([1, null, 3]))
    await user.click(screen.getByText("Show data"))
    const rows = within(screen.getByRole("table")).getAllByRole("row")
    // Newest first, so the middle point is the third row.
    expect(rows[2]).toHaveTextContent("–")
  })

  it("reads a value with the arrow keys and announces it", async () => {
    const user = userEvent.setup()
    render(chart([10, 20, 30]))
    const svg = screen.getByRole("img", { name: /^CPU:/ })
    expect(svg).toHaveAttribute("tabindex", "0")
    expect(screen.getByTestId("readout")).toHaveTextContent("Hover or focus")

    svg.focus()
    await user.keyboard("{ArrowLeft}")
    // The cursor starts on the newest point, so one step left is the middle one.
    expect(screen.getByTestId("readout")).toHaveTextContent("CPU average 20%")
    expect(screen.getByRole("status")).toHaveTextContent("CPU average 20%")

    await user.keyboard("{Home}")
    expect(screen.getByTestId("readout")).toHaveTextContent("CPU average 10%")
    await user.keyboard("{End}")
    expect(screen.getByTestId("readout")).toHaveTextContent("CPU average 30%")
    await user.keyboard("{Escape}")
    expect(screen.getByTestId("readout")).toHaveTextContent("Hover or focus")
  })

  it("says no data at a null point under the cursor", async () => {
    const user = userEvent.setup()
    render(chart([10, null, 30]))
    screen.getByRole("img", { name: /^CPU:/ }).focus()
    await user.keyboard("{ArrowLeft}")
    expect(screen.getByTestId("readout")).toHaveTextContent("CPU average no data")
  })

  it("moves a crosshair with the pointer", () => {
    render(chart([10, 20, 30]))
    const svg = screen.getByRole("img", { name: /^CPU:/ })
    svg.getBoundingClientRect = () => ({ left: 0, top: 0, width: 480, height: 150, right: 480, bottom: 150, x: 0, y: 0, toJSON: () => ({}) })
    // jsdom's PointerEvent drops the coordinates, so send the pointer event as a mouse event.
    fireEvent(svg, new MouseEvent("pointermove", { clientX: 480, bubbles: true }))
    expect(screen.getByTestId("crosshair")).toBeInTheDocument()
    expect(screen.getByTestId("readout")).toHaveTextContent("CPU average 30%")
    fireEvent(svg, new MouseEvent("pointerout", { bubbles: true, relatedTarget: document.body }))
    expect(screen.queryByTestId("crosshair")).not.toBeInTheDocument()
  })

  it("stacks bars and leaves a gap where every series is null", () => {
    const { container } = render(
      <TimeSeriesChart
        title="Requests"
        t={times(3)}
        step={60}
        format={pct}
        stacked
        series={[
          { key: "s2", label: "2xx", values: [5, null, 5] },
          { key: "s5", label: "5xx", values: [1, null, 0] },
        ]}
      />,
    )
    // Three points, but the null one draws nothing; the zero 5xx draws no sliver either.
    expect(container.querySelectorAll('rect[data-series="s2"]')).toHaveLength(2)
    expect(container.querySelectorAll('rect[data-series="s5"]')).toHaveLength(1)
  })

  it("says so when there is nothing to draw, and is not focusable", () => {
    render(chart(fill(3, null)))
    expect(screen.getByRole("img", { name: "CPU: no data" })).not.toHaveAttribute("tabindex")
    expect(screen.getByTestId("readout")).toHaveTextContent("No data in this range.")
  })
})
