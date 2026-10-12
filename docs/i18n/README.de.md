<div align="center">

# Nibwire

**Die Newsletter-Engine, die Sie einem KI-Agenten anvertrauen können.**<br>
Selbst gehostet · eine Binärdatei · eine Datei · sicher durch Design

[English](../../README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · **Deutsch**

</div>

---

**Nibwire** ist eine quelloffene, selbst gehostete Newsletter- und Mailinglisten-Engine. Sie wird als einzelne Go-Binärdatei ausgeliefert und speichert alles in einer SQLite-Datei: Abonnenten, Ausgaben, Versandverlauf und Statistiken. Sie bedienen Nibwire über die Kommandozeile oder überlassen es einem KI-Agenten über das **Model Context Protocol (MCP)**, und zwar innerhalb von Schutzgeländern, die kein Prompt aushebeln kann.

Kein Dashboard, das betreut werden muss. Kein Datenbank-Cluster, kein Cache, keine Queue. Ihre Liste, Ihre Daten, Ihr Server.

- 🤖 **Bereit für Agenten**: 27 MCP-Werkzeuge, eingeschränkte Zugangsdaten, Tagesbudgets und ein vollständiges Audit-Log
- 📦 **Eine Binärdatei, eine Datei**: der ganze Newsletter in einer Datenbank, gesichert mit einem Befehl
- 🛡️ **Sicher durch Design**: kein doppelter Versand, kein erneuter Versand einer verschickten Ausgabe, keine Mail an Abgemeldete
- ⚡ **Schnell und rücksichtsvoll**: paralleler Versand, der sich automatisch an die Limits Ihres Anbieters anpasst
- 💸 **Günstig im Betrieb**: läuft bequem auf dem kleinsten VPS
- 📬 **Alles, was Leser erwarten**: Abmeldung mit einem Klick, Double-Opt-in, öffentliches Archiv, eigene Domains
- 🔓 **Open Source**: AGPL-3.0, kommerzielle Lizenz verfügbar

## Warum Nibwire?

**Für Agenten gebaut, nicht nur für Menschen.** Entwurf, Prüfung, Testversand, Planung, Auswertung: Jeder Schritt ist ein MCP-Werkzeug. Gefährliches ist nicht bloß unerwünscht, sondern konstruktionsbedingt unmöglich. Es gibt kein Löschwerkzeug, die Vorabprüfungen lassen sich nicht überspringen, und das Budget der Zugangsdaten lässt sich nicht überschreiten.

**Radikal einfacher Betrieb.** Ein Prozess erledigt alles, eine SQLite-Datei enthält alles. Ein Update ist ein neues Image, ein Backup ist `nibwire db backup`.

**Fünf Garantien, die immer gelten:** keine Mail an abgemeldete, gebouncte oder beschwerdeführende Adressen; keine Ausgabe ohne bestandene Vorabprüfung; kein erneuter Versand einer bereits verschickten Ausgabe; kein Überschreiten des Tagesbudgets, auch nicht beim Pausieren heute und Fortsetzen morgen; und niemand erhält dieselbe Mail zweimal, selbst wenn der Server mitten im Versand abstürzt.

**Schnell und ein guter Bürger.** In Lasttests gingen 20.000 E-Mails in rund 45 Sekunden hinaus, ohne Duplikate und ohne Fehler. Das Tempo passt sich dabei automatisch an das an, was der Anbieter gerade erlaubt.

## Schnellstart

Probieren Sie alles in wenigen Minuten auf Ihrem Laptop aus. Ein eingebautes lokales Postfach sorgt dafür, dass keine Konten oder Zugangsdaten nötig sind und keine Mail Ihren Rechner verlässt.

```bash
git clone https://github.com/dayuer/nibwire.git
cd nibwire
KEEP=1 scripts/compose-smoke.sh
```

## Deployment und weitere Dokumentation

Produktions-Deployment, MCP-Anbindung, CLI-Referenz und Sicherheit finden Sie in der [englischen README](../../README.md#deploy). Ein Server mit 1 vCPU und 1 GB RAM genügt.

## Lizenz

**[GNU AGPL-3.0-only](../../LICENSE)**. Für eigene Newsletter, auch kommerziell, ist Nibwire kostenlos. Für den Betrieb einer veränderten Version als Dienst ohne Offenlegung der Änderungen oder die Einbettung in proprietäre Software gibt es eine **[kommerzielle Lizenz](../../COMMERCIAL-LICENSE.md)**. Nibwire begann als Go-Port von [Mailcheap](https://github.com/marclou/mailcheap) von Marc Lou (MIT).
