import { MutationCache, QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { RouterProvider } from "@tanstack/react-router"
import { StrictMode } from "react"
import { createRoot } from "react-dom/client"

import { api } from "@/lib/api"
import { applyTheme } from "@/lib/theme"
import { toast } from "@/lib/toast"
import { createAppRouter } from "@/router"
import "@/index.css"

// Before the first paint, so a dark-mode operator never sees a white flash.
applyTheme()

const rootElement = document.getElementById("root")
if (!rootElement) {
  throw new Error("root element missing from index.html")
}

// A mutation that names `meta.success` confirms itself with a toast; failures
// stay inline beside the control that was pressed.
const queryClient = new QueryClient({
  mutationCache: new MutationCache({
    onSuccess: (_data, _vars, _ctx, mutation) => {
      const message = mutation.meta?.success
      if (typeof message === "string") toast.success(message)
    },
  }),
  defaultOptions: {
    queries: {
      // The panel has one operator on one server; refetching on every window
      // focus would only add requests. A page that needs fresh data says so.
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
})

const router = createAppRouter(queryClient)

// A 401 on any non-login request means the session is gone. The guard handles
// it on navigation; this covers a request made while already on a page, such as
// a settings save after the CLI revoked the session.
api.setUnauthorizedHandler(() => {
  queryClient.clear()
  void router.navigate({ to: "/login", search: {}, replace: true })
})

createRoot(rootElement).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
