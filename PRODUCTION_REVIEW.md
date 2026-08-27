# Produktionsgranskning — camera-backup

**Datum:** 2026-08-27
**Omfattning:** Hela systemet, med extra fokus på de senaste dagarnas tillskott
(`status --json`, `--progress-json`, stall-detektorn, unreadable-hanteringen,
config-omskrivningen) samt kärnvärdena: källan är helig, allt kopieras, inget
på destinationen skrivs över eller raderas.

**Metod:** Genomläsning av README, CLAUDE.md och all källkod i `cmd/` och
`internal/`; build + `go vet` + hela testsviten (allt grönt); empiriska tester
mot genererad testdata för varje misstänkt brist. Inga fynd nedan är
spekulativa — alla med "Bevisat" är reproducerade mot binären.

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

> **Status 2026-08-27:** H1–H3 är åtgärdade på denna branch, med tester
> (`internal/verify/exitstatus_test.go`, nya `TestRunSync_*`-fall i
> `cmd/camera-backup/main_test.go`, `TestLoad_RejectsEmptyFileExtensions`).
> Omonterad destination vid `verify` behåller medvetet exit 0 — det
> dokumenterade "skipped, not failed"-fallet. M- och L-punkterna kvarstår.
