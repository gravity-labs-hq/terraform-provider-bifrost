resource "bifrost_queue" "correos" {
  name    = "correos"
  display = "Correos"
}

resource "bifrost_queue" "eventos" {
  kind = "jetstream"
  name = "eventos"
  jetstream = {
    subjects  = ["eventos.>"]
    retention = "workqueue"
    replicas  = 1
    storage   = "file"
  }
}
