import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import {
  IssueCertForm,
  PanelIssueForm,
  ProviderForm,
  TLSWarnings,
  UploadCertForm,
} from "@/components/TLSForms"
import { ApiError, type DNSProvider } from "@/lib/api"

const providers: DNSProvider[] = [
  { name: "cf", kind: "cloudflare", updated_at: "2026-01-01T00:00:00Z" },
]

const CERT = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
const KEY = "-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----"

describe("IssueCertForm", () => {
  const setup = (pending = false, error: unknown = null) => {
    const onSubmit = vi.fn()
    render(
      <IssueCertForm
        domain="a.test"
        defaultNames={["a.test", "www.a.test"]}
        providers={providers}
        pending={pending}
        error={error}
        onSubmit={onSubmit}
      />,
    )
    return onSubmit
  }

  it("starts from the domain and its aliases and submits over HTTP-01", async () => {
    const user = userEvent.setup()
    const onSubmit = setup()
    expect(screen.getByLabelText("Names on the certificate")).toHaveValue("a.test www.a.test")

    await user.click(screen.getByRole("button", { name: "Request certificate" }))

    expect(onSubmit).toHaveBeenCalledWith({ method: "http01", names: ["a.test", "www.a.test"] })
  })

  it("hides the wildcard option and the provider for HTTP-01", () => {
    setup()
    expect(screen.queryByLabelText(/Also cover \*\.a\.test/)).not.toBeInTheDocument()
    expect(screen.queryByLabelText("DNS provider")).not.toBeInTheDocument()
  })

  it("refuses a typed wildcard under HTTP-01 and sends nothing", async () => {
    const user = userEvent.setup()
    const onSubmit = setup()
    await user.type(screen.getByLabelText("Names on the certificate"), " *.a.test")
    await user.click(screen.getByRole("button", { name: "Request certificate" }))

    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByRole("alert")).toHaveTextContent("*.a.test is a wildcard")
  })

  it("offers a wildcard and a provider under DNS-01 and sends both", async () => {
    const user = userEvent.setup()
    const onSubmit = setup()
    await user.click(screen.getByRole("radio", { name: /DNS-01/ }))
    await user.click(screen.getByLabelText("Also cover *.a.test"))
    await user.type(screen.getByLabelText("Contact email"), "ops@a.test")
    await user.click(screen.getByRole("button", { name: "Request certificate" }))

    expect(onSubmit).toHaveBeenCalledWith({
      method: "dns01",
      names: ["a.test", "www.a.test", "*.a.test"],
      provider: "cf",
      email: "ops@a.test",
    })
  })

  it("requires a provider for DNS-01", async () => {
    const user = userEvent.setup()
    const onSubmit = setup()
    await user.click(screen.getByRole("radio", { name: /DNS-01/ }))
    await user.selectOptions(screen.getByLabelText("DNS provider"), "")
    await user.click(screen.getByRole("button", { name: "Request certificate" }))

    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByRole("alert")).toHaveTextContent("Choose a DNS provider")
  })

  it("requires a name", async () => {
    const user = userEvent.setup()
    const onSubmit = setup()
    await user.clear(screen.getByLabelText("Names on the certificate"))
    await user.click(screen.getByRole("button", { name: "Request certificate" }))
    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByRole("alert")).toHaveTextContent("at least one name")
  })

  it("shows the server's refusal", () => {
    setup(
      false,
      new ApiError(422, "invalid_input", "names: a.test does not resolve to this server"),
    )
    expect(screen.getByRole("alert")).toHaveTextContent("does not resolve to this server")
  })
})

describe("UploadCertForm", () => {
  const setup = (error: unknown = null) => {
    const onSubmit = vi.fn()
    render(<UploadCertForm pending={false} error={error} onSubmit={onSubmit} />)
    return onSubmit
  }

  it("needs a certificate and a key before it can be submitted", async () => {
    const user = userEvent.setup()
    setup()
    const button = screen.getByRole("button", { name: "Upload certificate" })
    expect(button).toBeDisabled()
    await user.type(screen.getByLabelText("Certificate"), "x")
    expect(button).toBeDisabled()
    await user.type(screen.getByLabelText("Private key"), "x")
    expect(button).toBeEnabled()
  })

  it("rejects text that is not PEM before it leaves the page", async () => {
    const user = userEvent.setup()
    const onSubmit = setup()
    await user.type(screen.getByLabelText("Certificate"), "hello")
    await user.click(screen.getByLabelText("Private key"))
    await user.paste(KEY)
    await user.click(screen.getByRole("button", { name: "Upload certificate" }))

    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByRole("alert")).toHaveTextContent("not a PEM certificate")
  })

  it("sends the three fields, then empties the private key and only the key", async () => {
    const user = userEvent.setup()
    const onSubmit = setup()
    await user.click(screen.getByLabelText("Certificate"))
    await user.paste(CERT)
    await user.click(screen.getByLabelText("Chain (optional)"))
    await user.paste(CERT)
    await user.click(screen.getByLabelText("Private key"))
    await user.paste(KEY)
    await user.click(screen.getByRole("button", { name: "Upload certificate" }))

    expect(onSubmit).toHaveBeenCalledWith({ certificate: CERT, chain: CERT, private_key: KEY })
    expect(screen.getByLabelText("Private key")).toHaveValue("")
    expect(screen.getByLabelText("Certificate")).toHaveValue(CERT)
  })

  it("turns autocomplete off on every box", () => {
    setup()
    for (const label of ["Certificate", "Chain (optional)", "Private key"]) {
      expect(screen.getByLabelText(label)).toHaveAttribute("autocomplete", "off")
    }
  })

  it("keeps the key box empty when the server refused the upload", () => {
    setup(new ApiError(422, "invalid_input", "private_key: does not match the certificate"))
    expect(screen.getByLabelText("Private key")).toHaveValue("")
    expect(screen.getByRole("alert")).toHaveTextContent("does not match the certificate")
  })
})

describe("TLSWarnings", () => {
  it("lists each warning the upload returned", () => {
    render(
      <TLSWarnings
        warnings={[
          { code: "expires_soon", message: "The certificate expires in 9 days." },
          { code: "no_chain", message: "No intermediate certificate was supplied." },
        ]}
      />,
    )
    expect(screen.getByRole("status")).toHaveTextContent("2 warnings")
    expect(screen.getAllByRole("listitem")).toHaveLength(2)
  })

  it("renders nothing without warnings", () => {
    const { container } = render(<TLSWarnings warnings={[]} />)
    expect(container).toBeEmptyDOMElement()
  })
})

describe("ProviderForm", () => {
  const setup = (replacing: string | null = null) => {
    const onSubmit = vi.fn()
    render(
      <ProviderForm
        kinds={["cloudflare"]}
        replacing={replacing}
        pending={false}
        error={null}
        onSubmit={onSubmit}
        onCancelReplace={() => {}}
      />,
    )
    return onSubmit
  }

  it("takes the token in a password field and says how to scope it", () => {
    setup()
    expect(screen.getByLabelText("API token")).toHaveAttribute("type", "password")
    expect(screen.getByText(/Zone:DNS:Edit/)).toBeInTheDocument()
  })

  it("is empty when replacing: the stored token is never shown", () => {
    setup("cf")
    expect(screen.getByLabelText("API token")).toHaveValue("")
    expect(screen.getByLabelText("Name")).toHaveValue("cf")
    expect(screen.getByLabelText("Name")).toHaveAttribute("readonly")
  })

  it("sends the token once and clears the field", async () => {
    const user = userEvent.setup()
    const onSubmit = setup()
    await user.type(screen.getByLabelText("API token"), "tok-123")
    await user.click(screen.getByRole("button", { name: "Save provider" }))

    expect(onSubmit).toHaveBeenCalledWith({
      name: "cloudflare",
      kind: "cloudflare",
      apiToken: "tok-123",
    })
    expect(screen.getByLabelText("API token")).toHaveValue("")
  })

  it("rejects a name with capitals or spaces", async () => {
    const user = userEvent.setup()
    const onSubmit = setup()
    await user.clear(screen.getByLabelText("Name"))
    await user.type(screen.getByLabelText("Name"), "My CF")
    await user.type(screen.getByLabelText("API token"), "t")
    await user.click(screen.getByRole("button", { name: "Save provider" }))

    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByRole("alert")).toHaveTextContent("lowercase letters")
  })

  it("cannot be submitted without a token", () => {
    setup()
    expect(screen.getByRole("button", { name: "Save provider" })).toBeDisabled()
  })
})

describe("PanelIssueForm", () => {
  it("explains that HTTP-01 needs a site for the hostname", () => {
    render(
      <PanelIssueForm providers={providers} pending={false} error={null} onSubmit={() => {}} />,
    )
    expect(screen.getByText(/a site for this hostname/)).toBeInTheDocument()
  })

  it("sends one hostname and refuses a wildcard over HTTP-01", async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn()
    render(
      <PanelIssueForm providers={providers} pending={false} error={null} onSubmit={onSubmit} />,
    )

    await user.type(screen.getByLabelText("Panel hostname"), "*.a.test")
    await user.click(screen.getByRole("button", { name: "Request certificate" }))
    expect(onSubmit).not.toHaveBeenCalled()

    await user.clear(screen.getByLabelText("Panel hostname"))
    await user.type(screen.getByLabelText("Panel hostname"), "Panel.a.test")
    await user.click(screen.getByRole("button", { name: "Request certificate" }))
    expect(onSubmit).toHaveBeenCalledWith({ hostname: "panel.a.test", method: "http01" })
  })
})
