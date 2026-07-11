# Audit sécurité et performance de `dbsession`

Date de l'audit : 11 juillet 2026

Périmètre : code Go du dépôt, gestionnaire HTTP et backends SQLite, PostgreSQL et Memcached.

Hors périmètre : configuration réelle d'une infrastructure de production, test d'intrusion réseau, audit dynamique d'un serveur PostgreSQL/Memcached et audit en ligne des versions de dépendances.

## Résumé exécutif

Le dépôt part d'une base saine : identifiants générés avec `crypto/rand` (128 bits), validation stricte des identifiants reçus, requêtes SQL paramétrées, contrôle d'expiration au niveau du gestionnaire, cookies `HttpOnly` et `SameSite=Lax` par défaut, rotation explicite de l'identifiant, statements préparés, WAL SQLite, pools SQL configurables et tests de concurrence.

Les tests existants passent, y compris sous le détecteur de races, et `go vet` ne signale rien. Cela ne couvre toutefois pas plusieurs chemins de défaillance et limites de configuration. Les corrections les plus urgentes sont :

1. corriger la conversion du TTL Memcached (`0` signifie « sans expiration » et une valeur supérieure à 30 jours est interprétée comme une date Unix) ;
2. vider les `bytes.Reader` avant leur retour au pool, car ils conservent actuellement une référence vers les données de session ;
3. rendre `Manager.Close` idempotent et attendre la fin du worker avant de fermer le store ;
4. valider toute la configuration à la construction et éviter les `panic` dans les chemins HTTP ;
5. rendre la limite de taille obligatoire/cohérente sur tous les backends, en particulier Memcached ;
6. clarifier et durcir le modèle de concurrence de `Session`, dont la map publique contourne les verrous ;
7. réduire le coût et les risques opérationnels du nettoyage global périodique.

Priorités utilisées dans ce document :

- **P0** : correction immédiate, risque de sécurité, d'expiration incorrecte ou de panne en production ;
- **P1** : forte valeur, à traiter avant une adoption large en production ;
- **P2** : amélioration structurante ou optimisation à valider par mesure ;
- **P3** : durcissement et confort opérationnel.

## Architecture et flux analysés

Le cookie ne contient qu'un identifiant opaque de 32 caractères hexadécimaux. Les données sont sérialisées avec `encoding/gob`, puis conservées côté serveur : blob en base SQL ou enveloppe complète dans Memcached. `Manager.Save` prolonge l'expiration, persiste la session, puis émet le cookie. Un worker par `Manager` appelle périodiquement `Store.Cleanup`.

Le flux présente trois frontières de confiance :

- le cookie, contrôlé par le client mais validé avant accès au store ;
- le contenu du store, qui est désérialisé et doit donc être considéré comme potentiellement corrompu ;
- la configuration de déploiement (TLS, proxy inverse, DSN, disponibilité et isolation du backend).

## Points positifs à préserver

- `generateID` utilise 16 octets de `crypto/rand`, soit 128 bits d'entropie, puis efface le buffer temporaire (`manager.go:303-318`).
- `Manager.Get` refuse tout identifiant qui n'est pas exactement un hexadécimal minuscule de 32 caractères (`manager.go:120-145`).
- Les requêtes SQLite/PostgreSQL utilisent des paramètres et des statements préparés ; aucun chemin d'injection SQL évident n'a été trouvé.
- L'expiration est revérifiée dans le manager, indépendamment du backend (`manager.go:141-146`).
- `SameSite=None` force `Secure=true` (`manager.go:86-92`).
- `Regenerate` tente de supprimer la nouvelle session et invalide le cookie si la suppression de l'ancien identifiant échoue (`manager.go:227-254`).
- Le buffer d'encodage est effacé avant remise au pool (`pool.go:27-37`).
- SQLite active WAL et applique `busy_timeout`/`synchronous` à toutes les connexions via le DSN (`sqlite.go:43-86`).
- PostgreSQL et SQLite contrôlent la taille du blob avant décodage quand leur limite locale est configurée.

## Sécurité

### P0 — Corriger la sémantique d'expiration Memcached

**Constat.** `MemcachedStore.Save` convertit `time.Until(...).Seconds()` directement en `int32` (`memcached.go:80-96`). Le protocole Memcached donne deux sens particuliers à cette valeur : `0` signifie aucune expiration et toute valeur supérieure à 30 jours est traitée comme un timestamp Unix absolu.

**Impacts.**

- un TTL positif inférieur à une seconde est tronqué à `0` et l'entrée peut devenir permanente ;
- un TTL supérieur à 30 jours peut être interprété comme une date de 1970 et expirer immédiatement ;
- une durée extrême peut déborder lors de la conversion en `int32` ;
- le manager empêche normalement de servir une enveloppe dont `ExpiresAt` est dépassé, mais l'objet sans TTL peut rester dans le cache et consommer de la mémoire ; un utilisateur direct du store ne bénéficie pas de cette défense.

**Actions.**

- [x] Créer une fonction testée `memcachedExpiration(expiresAt, now) (int32, error)`.
- [x] Arrondir une durée positive vers le haut et imposer un minimum de 1 seconde.
- [x] Pour une échéance au-delà de 30 jours, envoyer le timestamp Unix absolu, après contrôle de plage.
- [x] Refuser les échéances non représentables plutôt que de laisser déborder l'entier.
- [x] Revérifier `ExpiresAt` dans `MemcachedStore.Get` et supprimer au mieux une entrée déjà expirée.
- [x] Ajouter des tests aux bornes : `<1 s`, `1 s`, exactement 30 jours, `30 jours + 1 s`, TTL négatif et dépassement `int32`.

### P0 — Ne pas conserver les blobs décodés dans `readerPool`

**Constat.** Les méthodes `Get` font `reader.Reset(data)` ou `reader.Reset(item.Value)`, puis remettent le reader dans le pool sans le réinitialiser (`sqlite.go:171-179`, `postgres.go:156-163`, `memcached.go:45-50`). Un `bytes.Reader` conserve la slice source dans sa structure.

**Impacts.** Le pool prolonge la durée de vie en mémoire de données de session potentiellement sensibles. Il retient également des blobs volumineux jusqu'à la réutilisation ou l'évacuation du pool par le GC.

**Actions.**

- [x] Avant chaque `readerPool.Put(reader)`, appeler `reader.Reset(nil)` dans un helper unique tel que `PutReader`.
- [x] Ajouter un test interne vérifiant que le reader remis au pool a une taille nulle et ne référence plus l'entrée.
- [x] Mesurer ensuite si le pool de readers apporte un gain réel ; un `bytes.Reader` est petit et sa mise en pool peut coûter plus qu'elle ne rapporte.

Mesure réalisée avec `BenchmarkGobDecodeReader`, cinq répétitions sur AMD Ryzen 5 3600 : les deux variantes prennent environ 18,1–18,4 µs/op. Le pool réduit toutefois systématiquement le coût de 206 à 205 allocations et de 9 024 à 8 980 octets par décodage. Il est donc conservé, avec `PutReader` pour supprimer la référence au blob avant remise au pool.

### P0 — Sécuriser le cycle de vie de `Manager`

**Constat.** `Close` ferme directement `stopChan`, puis le store (`manager.go:99-118`). Il n'attend pas que `cleanupWorker` soit sorti. Si le tick et la fermeture sont prêts simultanément, le worker peut appeler `Cleanup` pendant ou après la fermeture du store. Un deuxième appel à `Close` panique en fermant à nouveau le canal. Le README ferme en outre le store et le manager séparément, alors que `Manager.Close` ferme déjà le store.

**Impacts.** Panic au shutdown, requêtes sur une base fermée, erreurs intermittentes et responsabilité de fermeture ambiguë lorsque plusieurs managers partagent un store.

**Actions.**

- [x] Utiliser `sync.Once` pour une fermeture idempotente et un `sync.WaitGroup`/canal `done` pour attendre le worker avant `Store.Close`.
- [x] Décider et documenter qui possède le store : soit le manager le ferme, soit l'appelant le ferme, mais pas les deux implicitement.
- [x] Envisager `Close(ctx)` afin de borner l'attente d'un nettoyage en cours.
- [x] Tester double fermeture, fermeture pendant `Cleanup`, fermeture concurrente et store partagé.

### P0 — Valider la configuration avant de démarrer une goroutine

**Constat.** `NewManager` retourne toujours un pointeur et ne valide pas `Store`, les durées, la taille, le nom/scope du cookie ou les valeurs `SameSite` (`manager.go:37-96`). `time.NewTicker` panique pour un intervalle négatif. Un store `nil` panique à la première opération. Un TTL négatif, sub-seconde ou excessif produit des expirations/cookies incohérents. `MaxSessionBytes < 0` désactive silencieusement la limite.

**Actions.**

- [x] Introduire un constructeur validant, idéalement `NewManager(cfg) (*Manager, error)` ; préserver temporairement l'API avec un `MustNewManager` si nécessaire.
- [x] Refuser les stores nil, les TTL explicites négatifs ou sub-seconde, `CleanupInterval < 0`, `MaxSessionBytes < 0`, les valeurs `SameSite` inconnues et les noms de cookie invalides. `TTL == 0` conserve le défaut documenté de 24 heures.
- [x] Définir explicitement comment désactiver le cleanup avec `DisableCleanup`, sans passer de durée sentinelle à `NewTicker`.
- [x] Exiger un TTL compatible avec `MaxAge` et définir la politique pour les TTL inférieurs à une seconde.
- [x] Valider `CookiePath`/`CookieDomain`, les contraintes `__Host-`/`__Secure-`, et avertir dans la documentation contre un domaine trop large.

### P1 — Éviter le `panic` si la source aléatoire échoue

**Constat.** `Manager.New` panique si `generateID` échoue (`manager.go:290-300`). `Get` appelle `New` pour tout cookie absent, invalide, inconnu ou expiré. Une panne d'entropie devient donc un panic dans un chemin HTTP normal, alors que `Regenerate` sait déjà propager cette erreur.

**Actions.**

- [ ] Faire retourner une erreur à `New` et aux branches correspondantes de `Get`, ou proposer `NewContext`/`MustNew` avec une distinction explicite.
- [ ] Ajouter des tests d'échec de `rand.Reader` pour `New` et `Get`, pas uniquement pour `Regenerate`.
- [ ] Ne jamais réutiliser un ancien identifiant comme repli en cas d'échec aléatoire.

### P1 — Rendre la limite de taille uniforme et activée par défaut

**Constat.** La limite est optionnelle et existe indépendamment dans `Manager`, `SQLiteConfig` et `PostgreSQLConfig`. Memcached n'a pas de limite applicative en lecture ou en écriture. Le manager limite uniquement le gob de `Values`, tandis que Memcached réencode une enveloppe avec métadonnées ; la taille réellement envoyée est donc différente (`manager.go:163-183`, `memcached.go:65-96`). Les valeurs `0` signifient « illimité » partout.

**Impacts.** Consommation CPU/mémoire lors de l'encodage/décodage, erreurs tardives liées à la limite serveur Memcached, et comportement différent selon que le store est appelé directement ou via le manager. Un backend compromis peut fournir un gob pathologique ; une limite avant décodage réduit ce risque.

**Actions.**

- [ ] Définir une limite sûre par défaut (par exemple 64 KiB, à ajuster au cas d'usage) et un mécanisme explicite pour l'augmenter.
- [ ] Faire porter la limite canonique sur le blob final réellement persisté.
- [ ] Ajouter `MaxSessionBytes` à une nouvelle `MemcachedConfig` et contrôler `len(item.Value)` avant gob.
- [ ] Éviter trois configurations susceptibles de diverger, ou refuser leur incohérence à l'initialisation.
- [ ] Ajouter des tests de contenu malformé, limites exactes, dépassement d'un octet, maps vides et enveloppe Memcached.

### P1 — Fermer l'échappatoire de concurrence exposée par `Session.Values`

**Constat.** `Get`, `Set`, `Delete` et `Clear` utilisent un mutex, et `Manager.Save` verrouille la session. Mais `Values`, `ID`, `CreatedAt` et `ExpiresAt` restent exportés (`session.go:9-52`). Le README recommande justement l'écriture directe `session.Values[...] = ...`. Cette écriture contourne le verrou et peut provoquer une race avec `Save`. Les valeurs retournées par `Get` peuvent aussi être des maps/slices mutables partagées. Enfin `Regenerate` modifie `s.ID` hors verrou avant d'appeler `Save` (`manager.go:214-225`).

**Actions.**

- [x] Choisir un contrat clair : `Session` est réellement thread-safe, avec champs privés et accesseurs.
- [x] Rendre la map privée, fournir `Get/Set/Delete/ValuesSnapshot`, verrouiller aussi les métadonnées et faire persister un snapshot immuable.
- [x] Verrouiller toute la transition d'identifiant lors de `Regenerate` sans deadlock avec `Save`.
- [x] Documenter que les objets mutables stockés doivent être copiés ou ne pas être modifiés concurremment.
- [x] Mettre à jour README/doc.go pour documenter le contrat thread-safe.
- [x] Étendre les tests race à l'indépendance des snapshots, `Regenerate` contre `Save/Set`, et `Destroy` contre `Get/Set`.

### P1 — Renforcer la rotation contre la fixation et les courses multi-requêtes

**Constat.** La rotation sauvegarde le nouvel ID, envoie son cookie, puis supprime l'ancien (`manager.go:211-257`). Il existe une fenêtre où les deux identifiants sont valides. Les opérations ne sont pas atomiques ; en cas d'erreur ambiguë ou de timeout, le nettoyage « best effort » peut laisser l'une ou les deux sessions valides. Deux requêtes concurrentes peuvent également prolonger ou recréer un ancien état.

**Actions.**

- [ ] Étendre l'interface avec une primitive de rotation atomique ou transactionnelle par backend (`Rotate(ctx, oldID, newSession)`).
- [ ] Pour PostgreSQL/SQLite, effectuer insert/update et suppression dans une transaction ; contrôler le nombre de lignes supprimées.
- [ ] Pour Memcached, documenter la limite d'atomicité et envisager tombstone/version de session ou compare-and-swap.
- [ ] N'émettre le nouveau cookie qu'après validation complète de la rotation ; sur erreur, expirer explicitement le cookie avec `Expires` dans le passé.
- [ ] Ajouter une version/génération afin qu'une sauvegarde tardive d'une requête concurrente ne ressuscite pas une session révoquée.

### P1 — Durcir les cookies pour les déploiements derrière proxy

**Constat.** Sans option explicite, `Secure` dépend de `r.TLS != nil` (`manager.go:191-205`, et logique dupliquée dans `Regenerate`/`Destroy`). Derrière un proxy qui termine TLS, la requête Go est souvent HTTP et le cookie sort alors sans `Secure`. Faire confiance aveuglément à `X-Forwarded-Proto` créerait inversement un risque si les proxies ne sont pas filtrés.

**Actions.**

- [ ] Recommander et montrer `Secure=true` explicitement pour toute production HTTPS.
- [ ] Considérer `Secure=true` comme défaut de production, avec opt-out explicite pour le développement local.
- [ ] Si l'auto-détection proxy est ajoutée, n'accepter les headers forwardés que depuis une liste de proxies de confiance.
- [ ] Proposer le préfixe `__Host-` lorsque `Secure=true`, `Path=/` et aucun `Domain` ; valider ces contraintes.
- [ ] Centraliser la construction du cookie pour éviter les divergences entre Save/Regenerate/Destroy.
- [ ] Lors de la suppression, ajouter une date `Expires` passée en plus de `MaxAge=-1` pour les clients anciens.

### P1 — Définir une durée de vie absolue et une politique de rotation

**Constat.** Chaque `Save` remplace `ExpiresAt` par `now + TTL` (`manager.go:151-162`). Une session fréquemment sauvegardée peut donc vivre indéfiniment. `CreatedAt` est conservé mais jamais utilisé pour imposer un maximum.

**Actions.**

- [ ] Distinguer délai d'inactivité, durée de vie absolue et fréquence de rotation de l'identifiant.
- [ ] Borner `ExpiresAt` par `CreatedAt + AbsoluteTTL`.
- [ ] Régénérer périodiquement l'identifiant et obligatoirement après authentification/changement de privilège.
- [ ] Tester les limites temporelles avec une horloge injectée plutôt qu'avec `time.Now` dispersé.

### P2 — Protéger et documenter le contenu du store

**Constat.** Les données de session sont en clair dans SQLite/PostgreSQL/Memcached et ne portent pas de tag d'intégrité applicatif. Gob est décodé depuis le backend. La sécurité repose donc totalement sur la confidentialité et l'intégrité du store et de son transport.

**Actions.**

- [ ] Documenter le modèle de menace : compromission du store = lecture/modification des sessions.
- [ ] Exiger TLS et authentification côté PostgreSQL en production ; ne pas présenter `sslmode=disable` comme exemple de production.
- [ ] Isoler Memcached sur un réseau privé strictement filtré ; la bibliothèque actuelle ne configure ni authentification ni TLS.
- [ ] Envisager un chiffrement authentifié optionnel des blobs avec rotation/version de clés si le modèle de menace l'exige.
- [ ] Stocker le minimum de données sensibles ; préférer des identifiants/rôles courts aux secrets réutilisables.
- [ ] Versionner le format sérialisé et traiter proprement les données corrompues/incompatibles.

### P2 — Faire respecter les invariants par le schéma

**Constat.** Les tables acceptent tout texte comme ID et n'imposent pas de taille de blob ; `created_at`/`expires_at` ne sont pas `NOT NULL` sous SQLite (`sqlite.go:88-97`, `postgres.go:71-80`). Un appel direct au store contourne la validation du manager.

**Actions.**

- [ ] Ajouter des contraintes : longueur/format de l'ID, dates non nulles, `expires_at > created_at`, taille maximale du blob si raisonnable.
- [ ] Nommer l'index avec un nom spécifique à la table/package ; `idx_expires_at` est trop générique dans un schéma PostgreSQL partagé.
- [ ] Déplacer les migrations DDL hors du constructeur ou offrir un mode explicite, afin que l'application puisse fonctionner avec un rôle sans privilèges DDL.

### P2 — Respecter les contextes sur tous les backends

**Constat.** Les méthodes Memcached ignorent `ctx` (`memcached.go:33-124`). Les constructeurs SQL utilisent `Ping`/`Exec` sans contexte. Le cleanup possède un timeout de 30 secondes, mais il est inopérant pour Memcached (où le cleanup est certes vide) et ses erreurs sont jetées.

**Actions.**

- [ ] Fournir une configuration Memcached complète : timeout réseau, nombre maximal de connexions inactives et limite de taille.
- [ ] Documenter que l'API gomemcache ne permet pas une annulation par requête, ou choisir un client qui la supporte si c'est requis.
- [ ] Utiliser `PingContext`/`ExecContext` avec un timeout à l'initialisation SQL.
- [ ] Exposer les erreurs de cleanup via logger/callback/métrique.

### P3 — Défenses complémentaires

- [ ] Documenter que `SameSite` réduit le risque CSRF mais ne remplace pas un jeton CSRF pour les opérations sensibles.
- [ ] Ajouter un namespace configurable aux clés Memcached pour isoler applications/environnements et faciliter les migrations.
- [ ] Évaluer un identifiant de 256 bits si une politique interne l'impose ; 128 bits aléatoires sont déjà suffisants pour l'usage courant.
- [ ] Ajouter une procédure automatisée d'audit des dépendances (`govulncheck`) dans la CI, ainsi que Dependabot/Renovate.
- [ ] Ne pas journaliser identifiants de session, blobs, DSN ou valeurs sensibles ; fournir des hooks d'observabilité qui respectent cette règle.

## Performance

### Mesures de référence locales

Commande exécutée :

```text
go test -run '^$' -bench 'Benchmark(Manager_Save_Empty|SQLiteStore_(Save|Get))$' -benchmem -count=3
```

Résultats indicatifs sur AMD Ryzen 5 3600, base SQLite de benchmark locale :

| Benchmark | Temps/op approximatif | Mémoire/op | Allocations/op |
|---|---:|---:|---:|
| SQLite Save | 51–52 µs | 2 170 B | 40 |
| SQLite Get | 26–27 µs | 9 044–9 045 B | 204 |
| Manager Save vide | 8,1–8,3 µs | 854–872 B | 18 |

Ces nombres servent de baseline, pas de promesse : ils incluent les choix du driver `modernc.org/sqlite`, le matériel et les fixtures actuelles. Aucun benchmark PostgreSQL/Memcached réel n'a été exécuté faute de services externes garantis dans l'environnement d'audit.

### P0 — Éviter la rétention mémoire des pools

Le correctif `reader.Reset(nil)` décrit dans la partie sécurité est également une optimisation mémoire importante. `bufferPool` accepte par ailleurs tout buffer, quelle que soit sa capacité (`pool.go:14-18`, `27-37`). Une seule session exceptionnellement grosse peut faire conserver longtemps une allocation importante dans le pool.

- [ ] Ne pas remettre au pool les buffers dont `Cap()` dépasse un seuil borné lié à `MaxSessionBytes`.
- [ ] Réinitialiser les readers avant remise au pool, ou supprimer ce pool si les benchmarks ne montrent pas de gain.
- [ ] Ajouter des benchmarks `-benchmem` pour plusieurs tailles : vide, 1 KiB, limite, dépassement, 1 MiB.
- [ ] Profiler heap/allocations avant de conserver des micro-optimisations de pool.

### P1 — Supprimer la double sérialisation Memcached

Quand `Manager.MaxSessionBytes` est activé, `Manager.Save` encode `Values` et remplit `session.encoded`. SQLite/PostgreSQL réutilisent ces octets. Memcached les ignore et encode ensuite `sessionEnvelope`, ce qui double le CPU et les allocations (`manager.go:163-186`, `memcached.go:65-78`).

- [ ] Déplacer l'encodage final et la validation de taille derrière une primitive commune au store/codec.
- [ ] Utiliser un format d'enveloppe identique pour tous les backends, ou laisser chaque store retourner la taille finale au manager.
- [ ] Mesurer séparément coût gob, I/O réseau et verrou de session avant/après.
- [ ] Éviter de tenir `s.mu` pendant toute une I/O lente : capturer un snapshot sous verrou, puis encoder/persister hors verrou.

### P1 — Repenser le nettoyage périodique

Chaque manager lance une goroutine et un ticker, même pour Memcached où `Cleanup` est un no-op (`manager.go:94-113`, `memcached.go:117-120`). Dans une flotte, toutes les instances démarrées ensemble exécutent un `DELETE` global à cadence proche. PostgreSQL peut subir verrous, WAL et bloat ; SQLite bloque toutes les écritures via son mutex pendant le delete (`sqlite.go:238-245`). Les erreurs sont invisibles.

- [ ] Rendre le worker optionnel et ne pas le créer pour les stores à expiration native.
- [ ] Ajouter du jitter et, en environnement distribué, une élection/verrou consultatif pour qu'une seule instance nettoie.
- [ ] Supprimer par lots bornés avec pauses/limites temporelles ; mesurer lignes supprimées et durée.
- [ ] Pour PostgreSQL, surveiller autovacuum/bloat et envisager partitionnement temporel à très gros volume.
- [ ] Pour SQLite, choisir une taille de lot qui borne le temps sous `mu` et planifier checkpoint/VACUUM selon le profil d'écriture.
- [ ] Rendre le timeout de cleanup configurable et exposer succès, échecs et volume supprimé.

### P1 — Réduire les écritures et sérialisations inutiles

Tout appel à `Save` encode et réécrit le blob, même si seules l'expiration ou aucune donnée n'a changé. Les requêtes UPSERT actualisent toujours `data` et `expires_at` (`sqlite.go:109-115`, `postgres.go:92-98`).

- [ ] Ajouter un indicateur `dirty` contrôlé par les accesseurs de `Session`.
- [ ] Séparer `Touch` (expiration uniquement) de `Save` (données modifiées).
- [ ] Éviter le write amplification en ne sauvegardant que lorsque nécessaire, éventuellement avec une fenêtre de rafraîchissement du TTL.
- [ ] Attention : une politique de touch différé doit préserver la durée d'inactivité et la sécurité de révocation.

### P1 — Mesurer puis réduire les allocations du chemin `Get`

Le benchmark SQLite actuel montre environ 204 allocations et 9 KiB par lecture pour une petite session. Le chemin `QueryContext` + `Rows` a été choisi pour utiliser `sql.RawBytes`, mais il faut démontrer que ce compromis est gagnant (`sqlite.go:142-180`, `postgres.go:125-164`).

- [ ] Comparer par benchmark `QueryContext/RawBytes` à `QueryRowContext/[]byte` sur SQLite et PostgreSQL.
- [ ] Produire profils CPU/heap et traces d'allocations ; optimiser les principaux contributeurs seulement après mesure.
- [ ] Évaluer un codec plus simple et typé pour les charges réelles ; gob avec `map[string]any` favorise la flexibilité, pas nécessairement la vitesse ni la stabilité inter-version.
- [ ] Versionner le codec et prévoir une migration avant tout changement de format.

### P2 — Ajuster SQLite au profil réel

SQLite ouvre par défaut jusqu'à 16 connexions mais sérialise toutes les écritures avec un mutex de processus (`sqlite.go:35-40`, `198-245`). Plusieurs processus ne partagent pas ce mutex et dépendent de `busy_timeout`. Le commentaire du benchmark ignore même les écritures parallèles au lieu de mesurer leur dégradation.

- [ ] Ajouter des benchmarks concurrents non ignorés à 1, 2, 4, 8 et 16 goroutines, avec taux lecture/écriture représentatifs.
- [ ] Tester `MaxOpenConns=1` contre plusieurs lecteurs en WAL ; choisir des valeurs par défaut fondées sur ces mesures.
- [ ] Ajouter `ConnMaxIdleTime` si le besoin existe et valider la cohérence `MaxIdleConns <= MaxOpenConns`.
- [ ] Vérifier la valeur réellement retournée par `PRAGMA journal_mode=WAL`, notamment pour `:memory:` ; l'absence d'erreur ne garantit pas toujours le mode demandé.
- [ ] Construire le DSN avec un parseur plutôt qu'avec des recherches de sous-chaînes sensibles à la casse (`sqlite.go:47-63`).

### P2 — Ajuster PostgreSQL et la stratégie de schéma

- [ ] Benchmarker avec latence réseau réaliste, tailles de pool variées et saturation ; les valeurs 25/5 ne peuvent pas convenir universellement.
- [ ] Exposer/mesurer `db.Stats()` : attentes de connexion, durée, connexions ouvertes/inactives.
- [ ] Comparer `lib/pq` à `pgx`/stdlib sur le workload réel avant migration.
- [ ] Sortir `CREATE TABLE/INDEX` du démarrage normal pour réduire les contentions de déploiement et utiliser des migrations versionnées.
- [ ] Vérifier avec `EXPLAIN (ANALYZE, BUFFERS)` le cleanup et les lectures à grande volumétrie.

### P2 — Optimiser l'expiration paresseuse

`Manager.Get` remplace une session expirée par une nouvelle mais ne tente pas de supprimer immédiatement l'ancienne (`manager.go:141-146`).

- [ ] Envisager une suppression asynchrone/best effort de l'entrée expirée, avec file bornée et métriques.
- [ ] Ne pas ajouter une suppression synchrone au chemin de lecture sans benchmark : elle augmenterait la latence et la charge lors d'un pic d'expirations.
- [ ] Prévenir les tempêtes de création : la simple lecture d'une requête sans cookie crée déjà un ID aléatoire, même si la session ne sera jamais sauvegardée. Évaluer une création paresseuse de l'ID au premier `Save`.

## Qualité, tests et observabilité

### Couverture à ajouter en priorité

- [ ] Tests unitaires de toutes les validations de configuration et absences de panic.
- [x] Tests TTL Memcached aux frontières du protocole.
- [ ] Tests de `Close` idempotent/concurrent et de l'arrêt ordonné du worker.
- [ ] Tests race couvrant rotation, destruction et champs actuellement publics.
- [ ] Tests fuzz sur `isValidID` et surtout sur le décodage gob borné/corrompu pour chaque backend.
- [ ] Tests d'intégration PostgreSQL et Memcached en CI, avec versions supportées explicites.
- [ ] Tests de panne : timeout, connexion coupée après écriture ambiguë, suppression échouée, store fermé, horloge avancée/reculée.
- [ ] Test de compatibilité du format de session entre deux versions de la bibliothèque.
- [ ] Test de charge prolongé contrôlant heap, nombre de goroutines, pool SQL, latences p50/p95/p99 et volume de cleanup.

### CI recommandée

- [ ] `go test ./...`
- [ ] `go test -race ./...`
- [ ] `go vet ./...`
- [ ] fuzzing avec corpus conservé et budget borné ; campagne longue planifiée séparément.
- [ ] `govulncheck ./...` et mise à jour automatisée des dépendances.
- [ ] benchmarks de non-régression sur runner stable, en surveillant temps, octets et allocations/op sans seuils trop fragiles.
- [ ] matrice avec les versions Go effectivement supportées et services PostgreSQL/Memcached.

### Observabilité à concevoir sans fuite de secrets

- [ ] Compteurs par opération/backend : succès, miss, expiration, erreur, session trop grande, rotation échouée.
- [ ] Histogrammes de latence et taille sérialisée ; durée/nombre de lignes du cleanup.
- [ ] Statistiques du pool SQL et erreurs réseau Memcached.
- [ ] Aucun ID complet, blob, valeur de session ou DSN dans les logs/métriques ; utiliser au besoin un identifiant de corrélation non secret.

## Plan d'exécution proposé

### Lot 1 — Correctifs immédiats

- [x] TTL Memcached correct et testé.
- [ ] `PutReader` avec `Reset(nil)` ; seuil de capacité pour `PutBuffer`.
- [ ] validation du constructeur ; suppression des panic atteignables depuis HTTP.
- [ ] `Close` idempotent, worker attendu, propriété du store documentée.
- [ ] limite de taille uniforme, dont Memcached lecture/écriture.

### Lot 2 — Cohérence de sécurité

- [ ] API `Session` réellement thread-safe et snapshot lors de Save.
- [ ] rotation atomique/versionnée selon les capacités du backend.
- [ ] cookie builder unique, `Secure` explicite en production, suppression avec date passée.
- [ ] délai d'inactivité + durée absolue + stratégie de rotation.
- [ ] schémas contraints et migrations versionnées.

### Lot 3 — Performance mesurée

- [ ] baseline reproductible par taille de session et niveau de concurrence.
- [ ] suppression de la double sérialisation et des écritures inutiles.
- [ ] cleanup distribué, jitteré et batché.
- [ ] profils CPU/heap avant/après ; ajustement pools SQLite/PostgreSQL.

### Lot 4 — Exploitation et durcissement

- [ ] métriques/logging sûrs, tests de panne et intégration CI des backends.
- [ ] audit automatisé des dépendances.
- [ ] documentation du modèle de menace, du TLS backend, de CSRF et des limites de cohérence Memcached.

## Critères de sortie avant production sensible

- aucune expiration Memcached incorrecte sur les cas limites ;
- aucune panic déclenchable par configuration ou échec d'entropie dans un handler ;
- fermeture concurrente propre sous `-race` ;
- taille maximale appliquée avant tout décodage sur chaque backend ;
- cookies toujours `Secure` dans l'architecture de production ;
- rotation testée en présence de requêtes concurrentes et de pannes partielles ;
- données non retenues par les pools après usage ;
- cleanup observable, borné et sans pic synchronisé entre instances ;
- objectifs de latence/mémoire définis puis vérifiés par benchmarks représentatifs ;
- modèle de responsabilité documenté pour TLS, store, révocation et fermeture.

## Vérifications réalisées pendant l'audit

```text
GOCACHE=/tmp/dbsession-go-cache go test ./...       # succès
GOCACHE=/tmp/dbsession-go-cache go test -race ./... # succès
GOCACHE=/tmp/dbsession-go-cache go vet ./...        # succès, aucun diagnostic
```

L'absence d'alerte sur ces commandes signifie que les scénarios couverts sont sains ; elle ne valide pas les cas limites listés ci-dessus, dont plusieurs n'ont actuellement aucun test.
