# Traffic-Map: Reparaturbericht

Stand: 11.09.2026, Branch `milestone-5-map`. Kein Branchwechsel, Push, PR oder Merge.
Die beim Beginn vorhandenen Änderungen und unversionierten Traffic-Dateien wurden weiterbearbeitet.

## Befunde

- Die lokale `.env` enthielt keine ntopng-Schlüssel. Compose, `.env.example` und Backend kannten sie bereits; `setup.bat` erzeugte sie nicht und verlor sie beim Neukonfigurieren.
- Es gab keine Kartenbibliothek: Kontinente und Länderlinien waren frei gezeichnete SVG-Pfade. Die Projektionshöhe 100 passte nicht zur SVG-Höhe 55; auch die inverse Klickposition war inkonsistent.
- Frankfurt wurde ohne Benutzerentscheidung als Ursprung gesetzt. Standortwerte wurden nur auf endliche Zahlen, nicht auf gültige Koordinaten geprüft.
- Fehlerhafte Antworten und Erreichbarkeitsfehler liefen zusammen. Eine fehlende JSON-Struktur konnte wie eine erfolgreiche leere Antwort aussehen. Login-Weiterleitungen wurden nicht explizit erkannt.
- Es wurde nur eine Seite mit maximal 500 ntopng-Flows gelesen und anschließend auf 150 Gruppen gekürzt. Zusammengefasste Sessions zeigten nur die Ports einer einzelnen Session.
- Die Länderzuordnung umfasste lediglich 25 Länder. Fehlerboxen lagen außerhalb der Karte; gestrichelte Linien waren die einzige Animation.

## Umsetzung

Die bestehende Seite, Session-Authentifizierung und REST-Anbindung bleiben erhalten.
D3 Geo + topojson-client projizieren lokal gebündelte Natural-Earth-Länder im Maßstab 1:50m.
Die Kartendatei umfasst ungefähr 756 KB; sie wird pro Seitenlauf einmal geladen und verarbeitet.
Eine separate Canvas-Ebene verwendet stabile Pfade und inkrementelle Flow-Updates mit Ein-/Ausblenden,
bewegtem Kopf und kurzem Schweif, Throughput-Strichstärke und unterschiedlichen Richtungsfarben.
Datumsgrenzen werden getrennt. Light/Dark Mode, reduzierte Bewegung, Filter und kompakte Diagnosen sind berücksichtigt.

Standorte werden explizit manuell oder per optionaler Browser-Geolocation gesetzt und lokal gespeichert.
Es gibt keine Defaultposition. Validierung, Löschen und Fehler bei nicht verfügbarem Browser-Speicher sind berücksichtigt.

Der ntopng-Client liest die dokumentierte `flow/active.lua`-Antwort, prüft ihre Struktur,
behandelt Login-Redirects gesondert und begrenzt Pagination, Antwortgröße und Zeitbudget.
Bytezähler werden im Backend als Integer gelesen. Länderinformationen kommen aus den Flows bzw. optional aus `host/active.lua`.
Aggregierte Sessions behalten sämtliche Ports, Anzahl, Summen und stabile Gruppen-IDs.
Die Karte listet unplatzierbare Flows und kennzeichnet Teilmengen und veraltete Statistiken.

`nerve-server traffic-check` prüft dieselbe Konfiguration im laufenden API-Container ohne Datenbankzugriff.
Ausgegeben werden Status und Zähler, keine Zugangsdaten oder Flow-IP-Adressen.

## Konfiguration und manuelle Abnahme

Die vollständige Beispielkonfiguration, Docker-Befehle, direkte ntopng-REST-Abfragen,
Diagnosezustände und der manuelle Abnahmeablauf stehen in [README.md](../README.md#live-traffic-map-ntopng).
Die sechs Variablen sind:

- `NERVE_NTOPNG_URL`
- `NERVE_NTOPNG_USERNAME`
- `NERVE_NTOPNG_PASSWORD`
- `NERVE_NTOPNG_INTERFACE_ID` (Standard `0`, tatsächliche Interface-ID auswählen)
- `NERVE_NTOPNG_TIMEOUT` (Standard `5s`)
- `NERVE_TRAFFIC_LOCAL_CIDRS` (optional)

Nach einer Änderung der `.env` muss der API-Container neu erzeugt werden.
`localhost` darin bezeichnet den Container; für ntopng auf dem Docker-Desktop-Host ist typischerweise
`host.docker.internal` erforderlich. Benutzername, Passwort, URL und Interface müssen zum echten ntopng passen.

## Prüfungen

| Prüfung | Ergebnis |
| --- | --- |
| `go test ./...` | Bestanden mit Go 1.27.1, Windows/amd64 |
| `go vet ./...` | Bestanden |
| `go test -tags=integration -count=1 ./...` | Bestanden gegen temporäres PostgreSQL 18.4; keine übersprungenen DB-Tests |
| Zusätzliche ntopng-Tests | Login-Redirect, 401/403, 5xx, ungültige Antworten, leere Antwort, Pagination, Timeout, Ports, Integer-Bytes und Cache bestanden |
| Zusätzliche API-Integrationstests | Unterschiedliche Diagnosecodes und Session-Schutz bestanden |
| `npm run typecheck` | Bestanden |
| `npm run test:map` | Projektions-Roundtrip, Arc-Endpunkte, Datumsgrenze und Standortvalidierung bestanden |
| `npm run build` | Bestanden, einschließlich vorhandenem Static-Runtime-Check |
| Setup-Neukonfiguration | Isolierter Test: alle sechs Werte einschließlich wörtlicher `$`-/`#`-Passwortzeichen bleiben erhalten |
| Browser | Dark/Light-Karte, Standortmarker, Speicherung nach Reload, kompakte Meldung und Diagnosepanel mit echter NERVE-API geprüft |
| Mobile Ansicht | Bei 390 px geprüft; dabei gefundener Leerraum anschließend per CSS reduziert und Build erneut geprüft |
| Docker-Builds | Aufgerufen, nicht ausführbar: Docker ist auf diesem Rechner nicht installiert |
| 500-Flow-Browserlasttest | Test-Harness vorbereitet, Browser-Aufruf durch automatische Freigabeprüfung wegen Nutzungslimit abgelehnt; keine FPS-Zusage validiert |
| Echte ntopng-Instanz | Nicht verbunden: hier liegen keine ntopng-Zugangsdaten vor; API-Vertrag mit HTTP-Testservern geprüft |

Die temporäre Datenbank des letzten vollständigen Testlaufs wurde geordnet gestoppt.
Die Testwerkzeuge liegen im ignorierten `.nerve`-Verzeichnis; die Anwendung enthält keinen Demo-Flow-Provider.

## Grenzen

- Die Zielposition ist ein ungefährer Länderzentroid, keine genaue Host-Geolocation.
- Inbound/Outbound bezeichnet den Session-Initiator; ntopng liefert hier den gesamten bidirektionalen Session-Durchsatz. Die Statistiken sind keine getrennten Upload-/Download-Messwerte.
- Höchstens zehn Seiten à 500 Quell-Flows und 500 angezeigte Gruppen; Teilmengen sind gekennzeichnet. Paging einer laufenden Quelle ist keine atomare Momentaufnahme.
- Ohne sichtbare interne Adressen (etwa bei NAT-Beobachtung nur auf WAN) sind zusätzliche lokale CIDRs bzw. die Wahl des geeigneten Capture-Interfaces nötig.
- Ein Standort pro Browser; keine Multi-Site-Zuordnung und keine historische Flow-Datenbank.
- PostgreSQL-Integration wurde hier auf Version 18.4 geprüft; das Compose-Deployment verwendet PostgreSQL 17. Docker- und echte ntopng-Abnahme bleiben deshalb erforderlich.
