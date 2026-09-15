# Traffic-Map: Reparaturbericht

## Ergänzung: Sensor-Einrichtung vom 12.09.2026

Ausgangsbasis dieser Folgearbeit war Commit `278a050` auf `milestone-5-map`.
Die reparierte Kartenprojektion, Flow-Animation, Geolocation und REST-v2-Auswertung
wurden beibehalten. Das verbleibende Problem war die stillschweigende Voraussetzung
einer bereits installierten ntopng-Quelle und fehlende Angaben zur Capture-Sichtbarkeit.

Die Architektur trennt Windows-Discovery, NERVE-Dienste und Capture-Sensor. Docker
Desktop stellt keine Windows-NIC für libpcap bereit. Deshalb startet die normale
Compose-Konfiguration keinen irreführenden ntopng-Capture-Container. `setup.bat`
bietet eine externe Quelle oder ein vollständig vorbereitetes Paket für einen
nativen Linux-Sensor an; Zugangsdaten werden sicher erzeugt und zwischen Sensor/API
abgeglichen. Bestehende `.env`-Werte, Kommentare und Secrets bleiben erhalten.
Diagnostics enthält Capture-Modus, Interface und ausdrücklich begrenzte Sichtbarkeit.
Ein einzelnes ntopng-Interface kann anhand der echten REST-v2-Interfaceliste automatisch
aufgelöst werden; bei mehreren Interfaces ist eine eindeutige ID erforderlich.

Erfolgreich geprüft:

- Go Unit Tests, `go vet ./...` und Integrationstests gegen isoliertes PostgreSQL,
  einschließlich bestehender Discovery-Abläufe und neuer Diagnose-/Leak-Prüfungen.
- `npm run typecheck`, `npm run test:map`, Produktionsbuild einschließlich statischer
  Runtime-Prüfung.
- Windows PowerShell 5.1: neue `.env`, Wiederholung, Secret-Persistenz, externe Quelle,
  Literal-Passwort, Fehler ohne Änderung der `.env`, Erhalt benutzerdefinierter Werte.
- Compose-Validierung der Standardinstallation und des erzeugten Sensorpakets.
- Echte Docker-Builds: API, Web, Linux-Sensor und Windows-Discovery-Agent.
- Echte ntopng-Version 7.0.260911 im isolierten Docker-Netzwerk: Account-Bootstrap,
  Reader-Login, Ablehnung des Default-Passworts, Authentifizierungsfehler, Healthcheck,
  echte HTTPS-Verbindung als Flow bis zur NERVE-API, keine Secrets in Diagnose/Logs.
- Frische, getrennte Compose-Installation mit Bootstrap/Anmeldung, korrektem
  Missing-Sensor-Zustand, erneutem Setup und Dienstneuerstellung bei erhaltener Sitzung
  und Datenbank. Ausschließlich die eigenen Testressourcen wurden danach entfernt.
- Produktionseinstieg des Sensors weist den tatsächlichen WSL2-Kernel ausdrücklich ab.

Die Docker-Laufzeit lag außerhalb des PATH und wurde unter der lokalen
Docker-Desktop-Installation gefunden. Der zunächst angenommene ntopng-Tag `stable`
existiert nicht; das tatsächlich verfügbare offizielle Image ist jetzt per Digest
gebunden. Der reale Container-Test deckte zudem erforderliche Datenverzeichnisrechte
für den eingeschränkten ntopng-Benutzer auf; diese wurden korrigiert.

Nicht nachgewiesen: physische Linux-NIC-/SPAN-/TAP-Erfassung, Windows-Host-Capture,
vollständiges LAN-Monitoring, ARM-Sensorbetrieb und ein manueller Doppelklick-Durchlauf
von `start.bat` samt Start des nativen Discovery-Agenten. Der isolierte Container-Test
belegt ausdrücklich nur den Traffic seines eigenen Netzwerk-Namespace. Die frühere
Karten-Visualprüfung unten ist kein neuer visueller Browser-Test dieser Folgearbeit.

Einrichtung, Sichtbarkeitsmatrix, Ports, Datenschutz, Diagnosebefehle und manuelle
Capture-Abnahme: [Traffic sensor setup](TRAFFIC_SENSOR_SETUP.md).

---

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

## Grenzen der damaligen ntopng-Reparatur

- Die Zielposition ist ein ungefährer Länderzentroid, keine genaue Host-Geolocation.
- Inbound/Outbound bezeichnet den Session-Initiator; ntopng liefert hier den gesamten bidirektionalen Session-Durchsatz. Die Statistiken sind keine getrennten Upload-/Download-Messwerte.
- Höchstens zehn Seiten à 500 Quell-Flows und 500 angezeigte Gruppen; Teilmengen sind gekennzeichnet. Paging einer laufenden Quelle ist keine atomare Momentaufnahme.
- Ohne sichtbare interne Adressen (etwa bei NAT-Beobachtung nur auf WAN) sind zusätzliche lokale CIDRs bzw. die Wahl des geeigneten Capture-Interfaces nötig.
- Ein Standort pro Browser; keine Multi-Site-Zuordnung und keine historische Flow-Datenbank.
- PostgreSQL-Integration wurde hier auf Version 18.4 geprüft; das Compose-Deployment verwendet PostgreSQL 17. Docker- und echte ntopng-Abnahme bleiben deshalb erforderlich.

## Native Windows-Erweiterung, 15. September 2026 — Abnahme offen

Die bisherige Grenze war die fehlende Windows-Host-Quelle. Ergänzt wurden ein
separater `nerve-sensor.exe`, der offizielle Windows-Netzwerk-ETW-Manifestprovider,
ein eigener HMAC-authentifizierter Loopback-Eingang und die vorhandene
`traffic.Source`-Abstraktion als gemeinsamer Map-Vertrag. Discovery und ntopng
wurden nicht ersetzt. Details: [Windows Traffic Sensor](WINDOWS_TRAFFIC_SENSOR.md).

Setup erzeugt ein separates 256-Bit-Sensor-Token und wählt automatisch einen freien
Loopback-Port. Bestehende Datenbank-/Discovery-Secrets und externe Quellen bleiben
erhalten. Nur der alte unkonfigurierte Windows-Standard wird einmalig migriert;
spätere bewusste Deaktivierung bleibt erhalten. Stock-Launcher werden gezielt
aktualisiert, Custom-Launcher bleiben bestehen. Start verwaltet einen geschützten
LocalService-Dienst; unveränderte Starts benötigen keine erneute Installation.
Eine verweigerte UAC-Installation verhindert weder Core noch Discovery.

Der normale Kernel-Systemlogger wurde unter LocalService tatsächlich mit
`0x80070005` abgewiesen. Der Windows-Manifestprovider funktionierte dagegen unter
demselben eingeschränkten Konto und lieferte reale TCP-/UDP- und IPv4-/IPv6-Flows
des erkannten Realtek-Ethernet-Adapters. Kein fremder Capture-Treiber wurde benötigt.
Unbenötigte proprietäre DIA-/KernelTraceControl-Helfer des NuGet-Pakets werden aus
dem finalen Artefakt ausgeschlossen; Lizenzhinweise und Hash-Lockfiles sind enthalten.

Der kontrollierte HTTPS-Test deckte anschließend einen Fehler bei TCP-Empfang auf:
Die Zuordnung darf nicht annehmen, dass dessen Tupel stets in Paket-Richtung
angeordnet ist. Der Code bestimmt jetzt die lokale Seite aus den tatsächlichen
NIC-Adressen und die Byte-Richtung unabhängig davon aus dem Ereignistyp. Der
ergänzte Unit-Test besteht. **Die reale Abnahme dieser letzten Korrektur ist offen:**
Windows beendete die dafür erforderliche Dienstaktualisierung mit „Vorgang wurde
durch den Benutzer abgebrochen“. Die Korrektur und das bereinigte finale Artefakt
wurden daher noch nicht als Windows-Dienst end-to-end geprüft.

| Aktuelle Prüfung | Tatsächliches Ergebnis |
| --- | --- |
| Go Unit Tests und `go vet ./...` | Bestanden |
| Go Integrationstests mit echtem isoliertem PostgreSQL | Bestanden |
| HMAC, Zeitfenster, Replay, Eingabevalidierung, Offline/Recovery und Geo-Ranges | Unit-Tests bestanden; synthetische Eingaben, kein Capture-Nachweis |
| Native `setup.bat` ohne Eingaben, Leerzeichenpfad und Wiederholung | Bestanden; vorhandene Werte/Secrets bleiben erhalten |
| Einmalige Migration und späterer Capture-Opt-out | Bestanden |
| `npm run typecheck`, `npm run test:map`, `npm run build` | Bestanden |
| Compose-Konfiguration und Loopback-/Frontend-Isolation | Bestanden |
| API-, Web-, Discovery-, Windows-Sensor- und Linux-Sensor-Docker-Builds | Bestanden |
| Reale Compose-Installation, Login/Session nach Neuerzeugung, Datenbankpersistenz | Bestanden gegen PostgreSQL 17 |
| Echter Windows-Start ohne Traffic-Quelle | Bestanden: Core und nativer Discovery-Agent laufen; INFO statt Abbruch |
| Echte ntopng-REST-Abnahme im isolierten Container | Bestanden: echte HTTPS-Flows, Leserzugriff, falsches Passwort und Default-Passwort-Ablehnung |
| Windows-ETW als LocalService | Reale Host-Flows nachgewiesen; keine vollständige TCP-Inbound-Abnahme der finalen Korrektur |
| Finale Windows-Download-/Browser-/Doppelstart-Abnahme | Nicht abgeschlossen wegen abgebrochener UAC-Aktualisierung; Browser-Harness vorbereitet, noch nicht erfolgreich ausgeführt |
| 500 aktive Flows / durchgehende FPS-Abnahme | Nicht gemessen; Zustände und Ausgabe sind begrenzt, keine Leistungszusage |

Die nativen Starttests unterdrücken ausschließlich die letzte kosmetische
Konsolenpause mit `NERVE_NO_PAUSE=1`; Docker, Discovery, Browser-Startbefehl und
Windows-Service-Aufrufe sind echt. Das ältere fokussierte CMD-Testdouble bleibt
ein zusätzlicher Kontrollflusstest. Das ntopng-Labor belegt keine SPAN-/LAN-Abdeckung.

Isolierte Testdienste und Container wurden gestoppt; die bestehende persönliche
Installation und ihre Datenbank wurden nicht zurückgesetzt. Der demand-start
Testdienst bleibt installiert und gestoppt. Testkonfiguration, Transfer-Metadaten
und Protokolle bleiben ausschließlich unter dem ignorierten `.nerve`.

Nächster Abnahmeschritt: `scripts/test-windows-capture.ps1` erneut ausführen und
die Windows-UAC-Aktualisierung bestätigen. Danach müssen beide Transfer-Richtungen,
Bytes, Änderung der Werte, die echte Map, Ausfall/Erholung, Doppelstart und Stop
erfolgreich geprüft sein. **Milestone 5 ist damit ausdrücklich nicht abgeschlossen.**
# Launcher upgrade regression — 2026-09-15

The migration in `d3ce9c2` recognized only one predecessor hash per launcher.
The actual local `start.bat` matched official commit `278a050`, so setup wrongly
preserved it as custom. That launcher skipped the native sensor entirely.

Setup now uses a bundled, provenance-backed catalog of all 14 distinct official
historical launcher contents and a schema-versioned local installed-content baseline
in `.nerve/launchers.json`. Future upgrades compare against that baseline; edited
scripts are retained with an INFO message. No marker-only overwrite or Git runtime
dependency is introduced. Custom scripts also remain protected when the compatibility
`-RefreshLaunchers` switch is passed. Templates, capture implementation, discovery,
environment-generation logic and database lifecycle were not changed.

Executed successfully on Windows:

- `scripts/test-setup-bat.ps1`: real CMD/PowerShell file invocation without input,
  paths with spaces, occupied-port fallback, second setup, retained secrets/values,
  actual `278a050` upgrade, future baseline upgrade and two kinds of custom launcher.
- `scripts/test-launcher-history.ps1`: all 14 catalog entries against their Git
  provenance, actual migration, newline normalization and malformed-state fallback.
- `scripts/test-setup.ps1`: configuration, external/Linux source and secret persistence.
- `scripts/test-compose.ps1`: Compose validation and configuration isolation.
- `scripts/test-start-traffic.ps1`: focused CMD completion test with CLI/browser doubles.
- `scripts/test-windows-capture.ps1`: actual legacy launchers seeded before normal
  setup; no manual refresh after setup. Docker builds, native startup and protected
  sensor installation/update completed. Real Ethernet ETW HTTPS traffic matched curl
  endpoints and inbound/outbound byte checks. Flow changes, offline TTL, recovery,
  second start without duplicate sensor, discovery and retained session passed.
  Launcher `traffic-check` returned `connected`. Real Edge login/map tests passed
  with mapped real flows, stored manual location and dark/light/mobile screenshots.
  Stop and isolated cleanup passed; no user database was removed.
- Normal workspace `cmd.exe /d /c setup.bat` upgraded the actual old local launcher;
  `.env` remained byte-identical. `git diff --check` passed.

The first sandboxed Compose invocation could not locate Docker; the explicit local
Docker executable succeeded with authorized access. Go/frontend source was unchanged;
their standalone unit/vet/typecheck suites were not repeated for this launcher fix.
The Docker production builds and real browser test above did run. This is automated
native Windows acceptance evidence, not a claim that the user's final Explorer
double-click acceptance is complete. Milestone 5 remains open pending that acceptance.
# Geography and local-site provenance — 2026-09-15

The old map restored an unversioned localStorage coordinate pair solely because its
numbers were in range. No source/time existed, so the reported Atlantic value could
not be authenticated or attributed to a particular historical user/browser action.
There is no matching hardcoded default in the inspected source/history. The fix retains
legacy coordinates as review candidates and requires explicit confirmation before
drawing an origin. New records include provenance, timestamp and browser accuracy;
manual locations persist and browser fixes are only requested by an explicit action.

Both native Windows and ntopng flow snapshots now receive local DB-IP City Lite MMDB
enrichment. Country-only fallbacks use 235 Natural Earth representatives, all verified
inside their own rendered country geometry. Unknown geography stays unlocated/listed.
City/region estimates are unchanged, approximate and labelled; lines never claim a
physical route. No extra traffic-IP requests or payload storage were introduced.

Validation performed:

- `go test ./...` and `go vet ./...` passed, with `NERVE_TEST_CITY_DATABASE` pointing
  to the actual pinned September MMDB. Real IPv4/IPv6 decoding, invalid database,
  city/region selection, country-only fallback, unknown location, source immutability,
  stable IDs/counts and empty-array serialization are covered.
- Go integration tests (`go test -tags=integration -count=1 ./...`) passed against an
  isolated PostgreSQL instance, including existing auth/discovery/ntopng/sensor tests.
- `npm run typecheck`, `npm run test:map`, `npm run build` passed. All country points
  are tested against bundled Natural Earth geometry; local provenance/migration tests
  cover absent, invalid, unknown-source, legacy, manual, stale-browser and unset data.
- `scripts/test-setup-bat.ps1`, `scripts/test-setup.ps1`, `scripts/test-compose.ps1`
  passed. An initial sandboxed batch test could not atomically replace a test file;
  the authorized normal-access rerun passed without changing setup implementation.
- `scripts/test-windows-capture.ps1` exercised actual Docker API/web/discovery/sensor
  builds and native setup/start, real ETW HTTPS flows, live City Lite results, sensor
  offline/recovery and duplicate-start protection. Real Edge tests covered absent and
  Atlantic/invalid legacy sites, explicit browser estimation, manual persistence,
  dark/light/mobile screenshots, discovery and retained authentication. No traffic
  route mocks were used. Browser geolocation coordinates were explicitly supplied by
  the isolated test context, not claimed as the user's physical location.

See [TRAFFIC_GEOLOCATION.md](TRAFFIC_GEOLOCATION.md) for sources, CC BY 4.0 attribution,
build-time updates, memory cost, migration and remaining limits. Automated validation
does not close Milestone 5; the user's real visual acceptance remains outstanding.
