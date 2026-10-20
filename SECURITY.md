# Política de seguridad

## Versiones con soporte

Se corrigen vulnerabilidades en la última versión publicada (`vX.Y.Z` más reciente).

## Cómo reportar una vulnerabilidad

**No abras un issue público.** Usa el canal privado de GitHub:
<https://github.com/gravity-labs-hq/terraform-provider-bifrost/security/advisories/new>
(«Report a vulnerability» en la pestaña Security del repositorio).

Si no puedes usar GitHub, abre una conversación privada con los propietarios de la organización `gravity-labs-hq` (perfil de la organización > People) indicando solo que tienes un reporte de seguridad; te daremos un canal privado. No publiques los detalles.

Incluye: versión del provider, versión de Bifröst, pasos para reproducir e impacto. Responderemos en un plazo de 5 días laborables y
coordinaremos la divulgación contigo; te citaremos en la nota de la corrección si lo deseas.

## Alcance

Entran: fugas de credenciales en logs o en el state, saltos de los límites de acceso del provider, validación TLS, firma de los artefactos de release.
Quedan fuera: vulnerabilidades de la propia plataforma Bifröst (repórtalas en <https://github.com/gravity-labs-hq/bifrost/security/advisories/new>).
