import * as React from "react"
import * as TabsPrimitive from "@radix-ui/react-tabs"

import { cn } from "@/lib/utils"

/** The selected tab, for TabsContent to hide the panels that are not it. */
const TabsValue = React.createContext<string | undefined>(undefined)

function Tabs({
  className,
  value,
  defaultValue,
  onValueChange,
  ...props
}: React.ComponentProps<typeof TabsPrimitive.Root>) {
  // The selection is mirrored here so a kept-mounted panel can tell whether it is
  // the shown one; Radix owns the state either way.
  const [current, setCurrent] = React.useState(value ?? defaultValue ?? "")
  const selected = value ?? current
  return (
    <TabsValue.Provider value={selected}>
      <TabsPrimitive.Root
        data-slot="tabs"
        className={cn("flex flex-col gap-3", className)}
        {...props}
        value={value}
        defaultValue={defaultValue}
        onValueChange={(next) => {
          setCurrent(next)
          onValueChange?.(next)
        }}
      />
    </TabsValue.Provider>
  )
}

function TabsList({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      data-slot="tabs-list"
      className={cn("border-border flex gap-1 border-b", className)}
      {...props}
    />
  )
}

function TabsTrigger({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      data-slot="tabs-trigger"
      className={cn(
        "text-muted-foreground hover:text-foreground -mb-px border-b-2 border-transparent px-2.5 py-1.5 pointer-coarse:py-3 text-sm font-medium outline-none",
        "focus-visible:ring-ring/50 rounded-t-md focus-visible:ring-[3px]",
        "data-[state=active]:border-primary data-[state=active]:text-foreground",
        className,
      )}
      {...props}
    />
  )
}

function TabsContent({ className, ...props }: React.ComponentProps<typeof TabsPrimitive.Content>) {
  // Radix's `forceMount` passes every panel `hidden: false`, so a panel kept
  // mounted that way stays visible and every tab's body stacks. Hidden is decided
  // here instead, so a panel can keep its state without being shown.
  const active = React.useContext(TabsValue) === props.value
  return (
    <TabsPrimitive.Content
      data-slot="tabs-content"
      className={cn("outline-none", className)}
      {...props}
      hidden={!active}
    />
  )
}

export { Tabs, TabsContent, TabsList, TabsTrigger }
