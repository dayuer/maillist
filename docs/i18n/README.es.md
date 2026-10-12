<div align="center">

# Nibwire

**El motor de newsletters que puedes confiar a un agente de IA.**<br>
Autoalojado · un solo binario · un solo archivo · seguro por diseño

[English](../../README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · **Español** · [Deutsch](README.de.md)

</div>

---

**Nibwire** es un motor de newsletters y listas de correo de código abierto y autoalojado. Se distribuye como un único binario de Go que guarda todo en un solo archivo SQLite: suscriptores, números, historial de envíos y estadísticas. Lo manejas desde la línea de comandos, o dejas que un agente de IA lo haga por ti mediante el **Model Context Protocol (MCP)**, siempre dentro de límites que ningún prompt puede saltarse.

Sin panel que vigilar. Sin clúster de base de datos, caché ni colas que operar. Tu lista, tus datos, tu servidor.

- 🤖 **Listo para agentes**: 27 herramientas MCP, credenciales con permisos acotados, presupuestos diarios y registro de auditoría completo
- 📦 **Un binario, un archivo**: toda tu newsletter vive en una base de datos y se respalda con un solo comando
- 🛡️ **Seguro por diseño**: nunca envía dos veces, nunca reenvía un número ya enviado y nunca escribe a quien se dio de baja
- ⚡ **Rápido y respetuoso**: envío concurrente que se adapta solo a los límites de tu proveedor
- 💸 **Barato de operar**: funciona holgado en el VPS más pequeño
- 📬 **Todo lo que esperan tus lectores**: baja con un clic, doble confirmación, archivo público, dominios propios
- 🔓 **Código abierto**: AGPL-3.0, con licencia comercial disponible

## ¿Por qué Nibwire?

**Pensado para agentes, no solo para personas.** Redactar, revisar, enviar una prueba, programar y leer el informe: cada paso es una herramienta MCP. Lo peligroso no está desaconsejado, sino que es imposible por construcción: no existe ninguna herramienta para borrar, no se pueden saltar las comprobaciones previas y no se puede superar el presupuesto de la credencial.

**Operación radicalmente simple.** Un proceso lo hace todo y un archivo SQLite lo guarda todo. Actualizar es descargar una imagen nueva; respaldar es `nibwire db backup`.

**Cinco garantías que siempre se cumplen:** nunca se escribe a direcciones dadas de baja, rebotadas o con quejas; ningún número sale sin pasar las comprobaciones; ningún número enviado puede reenviarse; nunca se supera el presupuesto diario, ni siquiera al pausar hoy y reanudar mañana; y nadie recibe el mismo correo dos veces, aunque el servidor falle a mitad del envío.

**Rápido y buen ciudadano.** En pruebas de carga se enviaron 20.000 correos en unos 45 segundos, sin duplicados ni fallos, ajustando la velocidad automáticamente a lo que el proveedor permite en cada momento.

**Listas sanas por defecto.** Los rebotes y las quejas se suprimen automáticamente. Los envíos se pausan solos si las tasas superan umbrales seguros, y una limpieza opcional envía un correo de "última oportunidad" a los lectores inactivos.

## Inicio rápido

Prueba todo en tu portátil en unos minutos. Usa un buzón local integrado, así que no necesitas cuentas ni credenciales y ningún correo sale de tu equipo.

```bash
git clone https://github.com/dayuer/nibwire.git
cd nibwire
KEEP=1 scripts/compose-smoke.sh
```

## Despliegue y más documentación

Los pasos de despliegue en producción, la conexión MCP, la referencia de la CLI y la seguridad están en el [README en inglés](../../README.md#deploy). Basta un servidor de 1 vCPU y 1 GB.

## Licencia

**[GNU AGPL-3.0-only](../../LICENSE)**. Usar Nibwire para tus propias newsletters, también con fines comerciales, es gratis. Si quieres ofrecer una versión modificada como servicio sin publicar los cambios, o integrarlo en software cerrado, consulta la **[licencia comercial](../../COMMERCIAL-LICENSE.md)**. Nibwire nació como un port a Go de [Mailcheap](https://github.com/marclou/mailcheap) de Marc Lou (MIT).
