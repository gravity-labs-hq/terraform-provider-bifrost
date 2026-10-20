# Programación de carga de un entorno: perfiles + horario semanal + excepciones.
# Los Deployments que nombran los perfiles (aquí `web`) deben existir en el entorno.
resource "bifrost_schedule" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  timezone    = "America/Santiago"
  base        = "noche" # rige donde el horario no dice nada

  profiles = {
    normal = { name = "Normal", color = "#2f6fdb", icon = "sun", workloads = { web = { min = 2, max = 3 } } }
    pico   = { name = "Pico", icon = "bolt", workloads = { web = { min = 2, max = 6, cpu_m = 200, cpu_limit_m = 400 } } }
    noche  = { name = "Noche", icon = "moon", workloads = { web = { min = 1, max = 1 } } }
  }

  # De lunes a viernes de 08 a 20 h rige «normal»; el sábado de 10 a 14 h, «pico». Una regla posterior pisa a una anterior.
  weekly = [
    { days = ["mon", "tue", "wed", "thu", "fri"], from = 8, to = 20, profile = "normal" },
    { days = ["sat"], from = 10, to = 14, profile = "pico" },
  ]

  # Un día especial (AAAA-MM-DD): pisa al horario semanal en las horas que cubre.
  exceptions = {
    "2026-12-24" = { label = "Nochebuena", ranges = [{ from = 0, to = 24, profile = "pico" }] }
  }
}
