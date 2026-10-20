resource "bifrost_replication_link" "pg" {
  name        = "tienda-pg"
  source      = "pg-prod"
  destination = "pg-replica"
  mode        = "one_way"
}
