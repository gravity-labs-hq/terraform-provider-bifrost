data "bifrost_apps" "todas" {}

output "apps" {
  value = data.bifrost_apps.todas.names
}
