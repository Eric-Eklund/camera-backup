# Produktionsgranskning — camera-backup

**Datum:** 2026-08-27 (kompletterande pass samma dag — se sista avsnittet)
**Omfattning:** Hela systemet, med extra fokus på de senaste dagarnas tillskott
(`status --json`, `--progress-json`, stall-detektorn, unreadable-hanteringen,
config-omskrivningen) samt kärnvärdena: källan är helig, allt kopieras, inget
på destinationen skrivs över eller raderas.

**Metod:** Genomläsning av README, CLAUDE.md och källkoden; build + `go vet` +
hela testsviten (allt grönt); empiriska tester mot genererad testdata för varje
misstänkt brist. Inga fynd nedan är spekulativa — alla med "Bevisat" är
reproducerade mot binären. Första passet läste alla datavägar i sin helhet och
UI-lagret punktvis; det kompletterande passet (sista avsnittet) täcker resten:
metadataparsern, hela TUI:n, `devices/`, `preview/`, plus fuzzning och en
hörnfallsgranskning av testsviten.

---

## Sammanfattning

Kärninvarianten — **programmet raderar eller skriver aldrig över en fil som
användaren lagt dit** — håller i kopieringsvägen. `safeCreate` öppnar alltid
med `O_EXCL`, kollisioner får `_N`-suffix, och de enda `os.Remove`-anropen i
kodbasen gäller destinationsfiler som samma operation själv skapade (misslyckad
kopiering, timeout, hash-mismatch). Stall-detektorn, unreadable-hanteringen och
den kirurgiska config-omskrivningen är väldesignade och vältestade.

Det som **inte** är produktionsklart är den andra kärnprincipen — *"en körning
som inte gjorde klart jobbet ska aldrig se klar ut"*. Tre vägar bryter mot den
i dag, alla verifierade mot binären. De bör åtgärdas innan drift.

---

## Höga brister (åtgärda före produktion)

### H1. `verify` exitar 0 även när filer har fel

`verify.Run` (`internal/verify/verify.go:46-87`) skriver ut problemen men
returnerar alltid `nil` — vid hash-mismatch, saknade filer, omonterade
destinationer och oläsbara källsökvägar.

**Bevisat:** korrumperade en NAS-kopia (samma storlek, ändrat innehåll):

```
  1 / 7 files have issues.
verify exit: 0
```

README lovar *"Every command exits non-zero when files failed"* och
arbetsflödet föreslår `verify` efter `sync` och "monthly" — dvs. exakt de
lägen där en cron/skript bara har exit-koden att gå på. Ett skript i stil med
`camera-backup verify && notify "backup OK"` rapporterar i dag en korrupt
backup som frisk. TUI:n påverkas inte (den läser bad-räknaren själv).

**Förslag:** returnera fel från `Run` när `bad > 0`, och överväg detsamma (eller
en egen exit-kod) när `!outcome.Clean()` — en körning som inte kunde titta på
allt är inte ett rent resultat, vilket är exakt resonemanget bakom `Outcome`.

### H2. `sync` med omonterad SSD rapporterar "NAS is already up to date"

`runSync` (`cmd/camera-backup/main.go:593-692`) kontrollerar bara att NAS är
tillgänglig. SSD-rötterna skannas via `WalkDual`, som tyst ger tomma listor
när roten inte finns — och en tom källa betyder "inget saknas".

**Bevisat:** med SSD-sökvägar som pekar på en obefintlig katalog:

```
  NAS is already up to date — nothing to copy.
sync exit: 0
```

Meddelandet påstår något om NAS som körningen aldrig kontrollerat. Detta är
samma felklass som commit b34a4d2 ("Stop reporting backups that were never
made") just åtgärdade för källskanningar — men för SSD:n som källa i `sync`.
Samma väg nås av `copy`:s fas 2. En användare vars externa SSD inte
monterades den morgonen får en grön rad och exit 0.

**Förslag:** kontrollera `config.RootAvailable` för SSD-rötterna i `runSync`
(och rapportera partiell tillgänglighet per kategori, som destinationssidan
redan gör). Felet ska ge non-zero exit. `nasSyncTasks` i TUI:n
(`internal/tui/ops.go:58`) har samma blinda fläck, men där skyddar
huvudskärmen delvis (badges visar SSD-status).

### H3. Tom `file_extensions` gör hela systemet blint — och nöjt

`config.Validate()` (`internal/config/config.go:277-307`) accepterar
`file_extensions = []`. Då ser `Walk` inga filer alls, och alla tre
skyddsnäten säger samtidigt att allt är klart.

**Bevisat:**

```
  Source files found :  0
  Missing from SSD  :  0
  Missing from NAS  :  0
status exit: 0
  All 0 files verified OK.
verify exit: 0
```

Detta är ordagrant scenariot CLAUDE.md beskriver som det farligaste ("det
tillstånd där någon formaterar ett kort"). TUI:ns settings-formulär avvisar en
tom lista (`toConfig`), men en handredigerad config.toml går rakt igenom
`Load`. Ett stavfel — t.ex. att raden råkar bli utkommenterad — räcker.

**Förslag:** flytta kravet till `config.Validate()`: minst en extension.
(Cross-key-regler ska enligt CLAUDE.md bo i `Validate`, inte i TUI-formuläret —
detta är redan husets egen regel.) Överväg också en varning när
`video_extensions` innehåller något som inte finns i `file_extensions`.

---

## Medel

### M1. `copy` icke-interaktivt: EOF tolkas som "nej", exit 0

`ui.AskYesNo` (`internal/ui/ui.go:153-158`) ignorerar läsfel; vid stängd stdin
(cron, pipe) blir svaret "nej" och fas 2 (SSD→NAS) hoppas över tyst med
exit 0. **Bevisat** med `copy </dev/null`. Filerna är på SSD:n så inget är
förlorat, men en schemalagd `copy` synkar aldrig till NAS och ser ändå lyckad
ut. Förslag: låt EOF/läsfel skilja sig från ett aktivt "nej" (avbryt med
tydligt meddelande, eller dokumentera att cron ska använda `copy` + `sync`
separat).

### M2. Aldrig-radera-invariantens lista är inte längre komplett

CLAUDE.md räknar upp de enda filer programmet får ersätta — config.toml,
loggar, egna ofullständiga kopior — och avslutar *"That is the complete
list."* `--progress-json` (nytt) skriver dock atomiskt över **valfri befintlig
fil** på den angivna sökvägen. **Bevisat:** en befintlig textfil ersattes utan
varning av `copy --progress-json <fil>`. Det är användarstyrt (som ett
`cp`-mål) och rimligt — men det är en fjärde kategori som ska in i
invariant-texten, och ett `O_EXCL`-tänk vore konsekvent: vägra skriva över en
befintlig fil som inte går att parsa som ett tidigare progress-dokument.
Samma sak gäller README:s "Safety guarantees"-avsnitt.

### M3. Kvarlämnat progress-dokument uppdateras inte av en tom körning

En `sync`/`dump` som inte har något att kopiera returnerar innan
`observeTo` öppnar dokumentet — filen från förra körningen ligger kvar
orörd med gamla siffror. `pid` + `updated_at` låter en noggrann läsare
upptäcka det, men en enkel widget visar förra veckans tillstånd. Förslag:
öppna och stäng dokumentet även för en tom batch (`total: 0`, `running:
false`), så att sökvägen alltid speglar senaste körningen.

---

## Låga / noteringar

- **L1. Loggfilskollision:** `createLogFile` använder `os.Create`
  (trunkerar). Två kommandon startade samma sekund delar tidsstämpel och det
  senare trunkerar det förras logg. `O_EXCL` + suffix vore i linje med
  resten av kodbasen.
- **L2. `files.done` i `--progress-json` räknar även misslyckade filer**
  (`progress.go:145-152`: `done++` oavsett `Err`). README-exemplet antyder att
  `done` är lyckade filer. Semantiken bör dokumenteras eller ändras till
  `done = lyckade`, annars visar `done/total` 100 % för en batch där allt
  misslyckades.
- **L3. Nyckel-inkonsekvens i progress-händelser:** seriella CLI-batchar
  rapporterar `Src.RelPath` (`copyop.go:534,549`) medan TUI:ns parallella pool
  rapporterar `DstRelPath` (`copyop.go:495-497`). Konsumenterna är i dag
  separata, så inget är trasigt — men den som återanvänder `FileProgress` får
  en överraskning.
- **L4. Instabila filer (`SplitStable`) ger exit 0:** de hoppas över med
  varning men påverkar inte exit-koden, till skillnad från oläsbara paths. En
  skriptad körning kan alltså se komplett ut medan filer väntades bort.
  Medvetet val (nästa körning tar dem, `status` visar dem som saknade) — men
  värt ett aktivt beslut: ska `counts.unstable > 0` synas i exit-koden?
- **L5. `nas_write_timeout_seconds = 0`** betyder inte "av" utan "default
  60 s" (`NASWriteTimeout`). Går inte att stänga av via config; om det är
  avsiktligt, dokumentera det i config-template.
- **L6. `safeCreate` skapar med 0666** (umask-styrt); progress-filen sätts
  explicit till 0644. Kosmetiskt, men konsekvens vore trevligt.

---

## Vad som granskades och håller

- **O_EXCL-invarianten:** alla tre kopieringsvägar (`copyVerified`,
  `copyFast`, `copyWithWriter`) går genom `safeCreate`; varje `os.Remove`
  gäller en fil som samma anrop skapade. Timeout-vägen raderar stubben först
  när den hängda skrivningen returnerat, och en stubbe som överlever
  processens död ersätts som `_N` — aldrig skrivs över. Korrekt.
- **Stall-detektorn:** mäter tystnad, inte total tid; `stopWriter` som både
  klocka och progress-sänke är en snygg konstruktion; `sendGuard` löser
  racet mellan övergivna kopior och stängd events-kanal. `stall_test.go`
  täcker den.
- **Unreadable-kedjan (b34a4d2):** `Walk` → `status`/`copy`/`dump`/`verify`
  hänger ihop; källskanningar exponerar listan, destinationsskanningar
  slänger den avsiktligt (asymmetrin är rätt). `incompleteSourceError` ger
  non-zero även när kopieringen lyckades. Enda luckan är H1 (verify-exit).
- **Basename+storlek+capture-time-symmetrin:** `MissingFromDest` och
  `verify.findCopy` fattar samma beslut, låst av `TestCopyAndVerifyAgree`.
- **`status --json`:** null-inte-noll konsekvent genomfört, fältnamn pinnade
  av `TestReport_Schema`, `unreadable` som alltid-beräknad int är rätt.
- **`config.Save`:** flerraders-arrayer, bottom-up-edits, `verifyRewrite`
  som backstopp, atomisk rename, rättighetsbevarande — gedigen.
- **`--progress-json`:** atomiska skrivningar, throttling med forcerade
  övergångar, misslyckade filers bytes räknas inte — genomtänkt (utöver
  M2/M3/L2 ovan).
- **TUI:** `CheckSpace` körs före varje batch; cancel via context låter
  pågående filer bli klara; batch-done efter sista progress-eventet.
- **Bygge/test:** `go build`, `go vet`, `go test ./...` — allt grönt.

## Rekommendation

Åtgärda H1–H3 före driftsättning; de är små, väl avgränsade ändringar
(en returnerad error, en tillgänglighetskoll, en valideringsregel) och alla
tre har naturliga hem i befintliga testfiler. M1–M3 bör beslutas aktivt
(fix eller dokumenterat val). L-punkterna är förbättringar utan brådska.

> **Status 2026-08-27:** H1–H3 är åtgärdade (PR #22, mergad), med tester
> (`internal/verify/exitstatus_test.go`, nya `TestRunSync_*`-fall i
> `cmd/camera-backup/main_test.go`, `TestLoad_RejectsEmptyFileExtensions`).
> Omonterad destination vid `verify` behåller medvetet exit 0 — det
> dokumenterade "skipped, not failed"-fallet. M- och L-punkterna kvarstår.

---

## Kompletterande pass — 2026-08-27

Första passet läste all kod som rör filer (copyop, scan, main, verify,
config/save, status, progress, ui, checksum, freespace) i sin helhet men
UI-lagret endast punktvis. Detta pass täcker resten rad för rad:
`scan/capture.go` (parserns inre ~570 rader), `tui/model.go`, `tui/view.go`,
`tui/settings.go`, `tui/devices.go`, `tui/msgs.go`, `tui/styles.go`,
`devices/` (alla filer), `preview/` (alla filer) — plus en hörnfallsgranskning
av testsviten och empiriska prov: fuzzning av metadataparsern, 0-bytefiler
genom hela kedjan, och TUI:n headless i tmux vid 120×40 och 80×24.

### Nytt fynd

**N1 (medel-hög — H2:s kvarvarande syskon): `status`/TUI/`--json` rapporterar
"up to date" mot en omonterad SSD.** H2-fixen gäller `sync`-kommandot, men
`status.Compute`:s ingen-kamera-gren (`status.go`, `SSDAvail()`-villkoret) och
TUI:ns synk-läge använder fortfarande `RootAvailable` — där *parent*-katalogen
räcker — för SSD:n i rollen som **källa**. En SSD vars rot inte finns men vars
mount point-förälder gör det (det klassiska omonterade fallet, t.ex.
`/mnt/ssd/Photos` med `/mnt/ssd` kvar som tom katalog) skannas som tom.
**Bevisat:** `status --json` ger `compared: "ssd"`, `missing_on_nas: 0`,
`ssd.photos.available: true`; TUI:n visar "All (0)" och `y` svarar "NAS is
already up to date." En waybar-panel som läser `missing_on_nas` visar "0 → NAS"
för en jämförelse som aldrig hände. Kopieringsvägen är säker (CLI-`sync`
vägrar numera; TUI:n kopierar bara det som skannats) — det är rapporteringen
som ljuger, vilket är exakt felklassen H2/b34a4d2 handlar om. Förslag: samma
`isDir`-regel som `runSync` fick, i `Compute`:s källgren och TUI:ns
synk-villkor; `compared` blir då `"none"` och räknarna null, som JSON-kontraktet
föreskriver.

### Mindre noteringar

- **N2 (låg):** QuickTime-tider saknar rimlighetsintervall: en korrupt
  `mvhd` v1 kan via Duration-overflow ge absurda (även negativa) årtal, och
  EXIF-parsern har golv (år ≥ 1900) men inget tak. Konsekvensen är enbart en
  bisarr datumkatalog — copy och verify parsar likadant, så jämförelsen förblir
  konsekvent och inget tappas eller dubbleras.
- **N3 (info):** TUI:ns progressrader nycklas på `DstRelPath`; två källfiler
  med samma basnamn och datum (mappöverrullning på samma kort) delar rad på
  progresskärmen. Räknarna stämmer — endast visningen slås ihop.
- **N4 (info):** JPEG-parsern hanterar inte 0xFF-fyllnadsbytes före en markör
  (tillåtet enligt spec, skrivs inte av kameror) — utfallet är bara
  modtime-fallback, aldrig fel data.

### Vad passet verifierade som robust

- **Metadataparsern (`capture.go`)** är defensivt skriven: begränsade loopar
  (`maxIFDEntries` 512, `maxHEIFItems` 4096), negativa offsets avvisas,
  `construction_method 1` avvisas i stället för att läsas som filoffset,
  atom-vandringen garanterar progress (storlek < header ⇒ fel), 64-bitars
  atomstorlekar som slår över blir negativa och avvisas. **Fuzzat:** ny
  `FuzzCaptureTime` (incheckad, seedad med alla containerformat) körde
  1,56 miljoner exekveringar utan panik, hängning eller obegränsad allokering
  — parsern tål en trasig kortläsares korrupta headrar.
- **0-bytefiler** (avbruten kamerawrite): kopieras, synkas och verifieras
  korrekt genom hela kedjan — empiriskt provat.
- **`devices/`:** probe per enhet i egen goroutine bakom deadline, arbetar på
  kopior (race-fritt när timeouten slår), allowlist av filsystem, korrekt
  mountinfo-parsning med oktala escapes, over-mount på samma mount point
  hanteras, bind-mountade filer filtreras bort.
- **`preview/`:** exiftool anropas utan skal med absoluta sökvägar (ingen
  argumentinjektion — sökvägar börjar alltid med `/`), all skalning är ren
  beräkning, misslyckade laddningar cachas.
- **TUI:n:** ✓SSD/✓NAS-kolumnen härleds ur exakt de missing-listor kopieringen
  byggs av (`absPathSet`), och NAS-kolumnen döljs helt när NAS inte är
  tillgänglig — inga falska bockar. `lineEditor.insert` använder
  full-slice-uttryck (ingen aliasing), config-byten sker på kopior.
  Headless-körning i tmux vid 120×40 och 80×24: korrekt rendering, inga
  radbrytningar, help/settings/devices-skärmarna fungerar.

### Testsvitens hörnfallstäckning

~150 beteendedrivna tester. Täckning: kärnpaketen 67–88 % (copyop 68 %,
scan 87 %, config 87 %, verify 86 %, progress 87 %); TUI 33 % och preview 44 %
är rendering respektive exiftool-beroende kod — rimligt. Sviten täcker de
svåra hörnen: kollisioner (`_N`-varianter åt båda håll), stall-detektorns
gränser, cancel före/mitt i batch, sena sends efter kanalstängning,
unreadable-kedjan, HEIF-avvisningsfallen, flerraders-arrayer i config-save,
och `TestCopyAndVerifyAgree` som låser copy/verify-symmetrin. Luckor utan
bugg bakom: `safeCreate`-uttömning (9999 varianter, praktiskt onåbar),
0-bytefallet (nu empiriskt provat, kunde bli test), och fuzzning — den sista
är stängd i och med `capture_fuzz_test.go`.

### Slutsats efter det kompletterande passet

Omdömet från första passet står sig: datavägarna är säkra och nu genomlästa
till 100 %, liksom hela UI-lagret. Det enda nya fyndet med produktionsrelevans
är **N1** — rapporteringssidans kvarvarande variant av H2 — som bör åtgärdas
med samma lilla `isDir`-regel. N2–N4 är medvetna avvägningar att lämna eller
städa vid tillfälle.

> **Status:** N1 är åtgärdat på denna branch, i alla tre skepnader. Under
> granskningen visade sig N1 även ha en **verify-variant**, den allvarligaste:
> utan kamera accepterade `verify` en omonterad SSD som auktoritet och svarade
> "All 0 files verified OK", exit 0. `StatusResult` skiljer nu på SSD:n som
> destination (`RootAvailable`, oförändrat) och som källa
> (`SSDPhotosReadable`/`SSDVideosReadable`, `isDir`): `status.Compute` och
> `NewReport` hoppar över jämförelsen (`compared: "none"`, räknare null),
> TUI:n visar "No camera, and the SSD is not mounted" i stället för "NAS is
> already up to date" och döljer All-fliken, och `verify` vägrar auktoriteten
> med fel och exit 1. Tester i status-, json-, verify- och tui-paketen pinnar
> beteendet; allt verifierat end-to-end mot binären, inklusive att normal
> drift är oförändrad.
