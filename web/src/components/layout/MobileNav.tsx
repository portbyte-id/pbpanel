import * as DialogPrimitive from "@radix-ui/react-dialog"

import { Sidebar } from "@/components/layout/Sidebar"

/**
 * The navigation below the `md` breakpoint: the same sidebar, in a drawer that
 * slides in from the left. Radix Dialog gives it the focus trap, Escape to
 * close and focus return to the menu button; picking a page closes it.
 */
export function MobileNav({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="bg-foreground/40 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:animate-in data-[state=open]:fade-in-0 fixed inset-0 z-50 md:hidden" />
        <DialogPrimitive.Content
          aria-describedby={undefined}
          className="bg-sidebar data-[state=closed]:animate-out data-[state=closed]:slide-out-to-left data-[state=open]:animate-in data-[state=open]:slide-in-from-left fixed inset-y-0 left-0 z-50 w-64 max-w-[85vw] shadow-xl outline-none md:hidden"
        >
          <DialogPrimitive.Title className="sr-only">Navigation</DialogPrimitive.Title>
          <Sidebar mobile onNavigate={() => onOpenChange(false)} />
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}
