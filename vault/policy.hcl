# Example Vault policy for Portscope
path "secret/data/portscope/*" {
  capabilities = ["read"]
}
