import { Button } from "@/components/ui/button"

function App() {
  return (
    <main className="flex min-h-svh flex-col items-center justify-center gap-6 p-8">
      <div className="flex flex-col items-center gap-2 text-center">
        <h1 className="text-3xl font-semibold tracking-tight">Server Panel</h1>
        <p className="text-muted-foreground max-w-md text-sm">
          Frontend scaffold is wired. The API, authentication and settings screens arrive in the
          foundation milestone.
        </p>
      </div>
      <Button disabled>Sign in</Button>
    </main>
  )
}

export default App
