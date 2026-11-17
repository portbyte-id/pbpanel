import { useId, useState } from "react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { ApiError } from "@/lib/api"
import { useInstallNode, useRuntimes, useUninstallNode } from "@/lib/queries"

/** What an operator may type: a major, major.minor or major.minor.patch. */
export function validNodeRequest(v: string): boolean {
  return /^\d{1,2}(\.\d{1,3}){0,2}$/.test(v.trim())
}

/** The Node.js versions the apps can run on, installed through nvm. */
export function RuntimesPage() {
  const id = useId()
  const { data, isPending, error } = useRuntimes()
  const install = useInstallNode()
  const uninstall = useUninstallNode()
  const [version, setVersion] = useState("")
  const [started, setStarted] = useState<string | null>(null)

  if (isPending) {
    return <p className="text-muted-foreground text-sm">Loading runtimes…</p>
  }
  if (error) {
    return (
      <p role="alert" className="text-destructive text-sm">
        {error instanceof ApiError ? error.detail : "The runtimes could not be loaded."}
      </p>
    )
  }

  const bad = version.trim() !== "" && !validNodeRequest(version)
  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    const v = version.trim()
    if (!validNodeRequest(v)) return
    install.mutate(v, {
      onSuccess: () => {
        setStarted(v)
        setVersion("")
      },
    })
  }

  return (
    <div className="flex max-w-3xl flex-col gap-6">
      <Card>
        <CardHeader>
          <CardTitle>Install Node.js</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <form onSubmit={submit} className="flex flex-wrap items-end gap-3" noValidate>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-version`}>Version</Label>
              <Input
                id={`${id}-version`}
                value={version}
                onChange={(e) => setVersion(e.target.value)}
                placeholder="24 or 22.11.0"
                autoComplete="off"
                spellCheck={false}
                aria-invalid={bad}
                aria-describedby={bad ? `${id}-version-error` : undefined}
                className="w-48"
              />
            </div>
            <Button type="submit" disabled={install.isPending || version.trim() === "" || bad}>
              {install.isPending ? "Starting…" : "Install"}
            </Button>
          </form>
          {bad && (
            <p id={`${id}-version-error`} role="alert" className="text-destructive text-xs">
              Use a version such as 24, 22.11 or 22.11.0.
            </p>
          )}
          {install.error && (
            <p role="alert" className="text-destructive text-sm">
              {install.error instanceof ApiError ? install.error.detail : "The install could not be started."}
            </p>
          )}
          {started && (
            <p className="text-sm" role="status">
              Installing Node.js {started}. It appears below when it is done; the output is in Jobs.
            </p>
          )}
          <p className="text-muted-foreground text-xs">
            {data.nvm_installed
              ? `Installed through nvm ${data.nvm_tag}.`
              : `nvm ${data.nvm_tag} is downloaded from GitHub the first time you install a version, so the server needs git and internet access.`}
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Installed</CardTitle>
        </CardHeader>
        <CardContent>
          {data.node.length === 0 ? (
            <p className="text-muted-foreground text-sm">No Node.js version is installed.</p>
          ) : (
            <ul className="divide-border flex flex-col divide-y">
              {data.node.map((n) => (
                <li key={n.version} className="flex flex-wrap items-center gap-3 py-3 text-sm first:pt-0 last:pb-0">
                  <span className="w-24 font-medium tabular-nums">{n.version}</span>
                  <code className="text-muted-foreground flex-1 truncate text-xs">{n.path}</code>
                  {n.used_by.length > 0 ? (
                    <Badge variant="secondary">Used by {n.used_by.join(", ")}</Badge>
                  ) : null}
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={n.used_by.length > 0 || uninstall.isPending}
                    title={n.used_by.length > 0 ? "Apps still run on this version." : undefined}
                    onClick={() => uninstall.mutate(n.version)}
                  >
                    Remove
                  </Button>
                </li>
              ))}
            </ul>
          )}
          {uninstall.error && (
            <p role="alert" className="text-destructive mt-3 text-sm">
              {uninstall.error instanceof ApiError ? uninstall.error.detail : "The removal could not be started."}
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
