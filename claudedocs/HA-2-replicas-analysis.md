# Analyse HA / 2 replicas — oci-storage

Date: 2026-04-27 · Périmètre: comportement multi-pod, partage volume, reprise activité, timeouts pull large images.

## 1. TL;DR — Verdict

L'app a **les briques de coordination** (Redis : LockManager, UploadTracker, ScanTracker) et un **backend S3** pour partager les données. Mais en l'état, passer à `replicas: 2` **ne livre pas un mode HA réel** : il reste 6 trous bloquants (🔴) et 5 trous importants (🟡). Le souci principal n'est pas le code Go, c'est **le packaging Helm + l'absence de single-flight inter-pod sur les pulls upstream**.

Pour rendre le mode 2-replicas réellement « si je perds un pod, le cluster continue » : suivre le plan d'action § 7.

## 2. Architecture observée

| Couche | Mode 1 replica (actuel) | Mode 2 replicas — supporté ? |
|---|---|---|
| Storage backend | Local PVC `RWO` 100 Gi | ✅ Si `s3.enabled=true` (Garage/MinIO/AWS) |
| Coordination | Noop locks, noop tracker | ✅ Si `redis.enabled=true` |
| Strategy | `Recreate` (downtime) | Auto-switch `RollingUpdate maxSurge=1 maxUnavailable=0` quand S3 |
| PVC | accessMode `ReadWriteOnce` | 🔴 **Pas modifié** — bloque le 2e pod si S3 désactivé |
| Service | ClusterIP simple | 🔴 Pas de session affinity |
| Ingress | Traefik | 🔴 Pas de sticky session, pas de body-size override |
| Anti-affinity | `affinity: {}` + `nodeSelector: kubernetes.io/hostname: ryzen` | 🔴 **Hard-pin sur 1 node = SPOF** |
| Probes | timeoutSeconds=1, periodSeconds=5 | 🟡 Trop agressif pour workload IO-lourd |
| Resources | 1 CPU / 2 Gi limit | 🟡 Sous-dimensionné pour pulls concurrents 100MB+ |

## 3. Findings — sévérité

### 🔴 Bloquants pour HA réelle

**B1. `nodeSelector` épingle à un seul host (`ryzen`) + `affinity: {}` vide**
- `helm/values.yaml:42` : `kubernetes.io/hostname: ryzen` → les 2 replicas atterrissent sur le même node. Si `ryzen` tombe, les 2 pods tombent — c'est exactement le scénario que tu décris (« cluster KO »).
- Pas de `podAntiAffinity` ni `topologySpreadConstraints` → même sans nodeSelector, K8s pourrait colocaliser.

**B2. PVC reste `ReadWriteOnce` même avec replicas > 1**
- `helm/values.yaml:62-64` : `accessModes: [ReadWriteOnce]`. Le template Deployment auto-switch en `RollingUpdate` quand S3 est activé (`deployment.yaml:16-21`), mais le PVC, lui, n'est pas conditionné. Si quelqu'un active `replicas: 2` **sans** S3 → le 2e pod reste en `ContainerCreating` indéfiniment (RWO = un seul mounter).
- Aucune validation Helm (NOTES.txt / values schema) pour empêcher ce mismatch.

**B3. Pas de single-flight inter-pod sur les pulls upstream du proxy**
- `src/pkg/handlers/oci.go:32-34` : `smallBlobSemaphore = make(chan struct{}, 5)` et `largeBlobSemaphore = make(chan struct{}, 7)` sont des variables Go **par-pod**.
- Conséquence avec 2 pods et 2 nodes pullant la même image en parallèle : chaque pod télécharge **indépendamment** le même blob de 5 Go depuis ghcr.io → 2× egress, 2× temp disk, 2× CPU.
- `src/pkg/handlers/oci_proxy.go:120-125` (« double-check after acquiring semaphore ») ne déduplique que **dans un même pod**.
- Pas de `coordination.LockManager` posé avant `proxyService.GetBlob(ctx, ...)`. Le code Redis est prêt pour ça, mais inutilisé sur ce chemin.

**B4. Reprise d'activité d'un upload chunked = 0**
- `src/pkg/handlers/oci.go:535-608 (PatchBlob)` : les chunks PATCH sont écrits dans `data/temp/<uuid>` qui est **local au pod** (`PathManager.GetTempPath` retourne un chemin absolu local, paths.go:31-35). Idem sur backend S3 (le temp staging reste local — `storage/s3.go:247-254`).
- Si pod-A reçoit POST + 5 PATCH puis crash, le client doit redémarrer le push **from scratch**. Le tracker Redis renvoie 409 si le client retombe sur pod-B (`oci.go:546-549`), mais il n'y a aucun mécanisme de reprise — le `.chunked` marker et les bytes accumulés sont perdus avec le pod.
- Pour une vraie reprise il faudrait soit (a) staging des chunks sur S3 multipart, soit (b) un PVC RWX dédié au temp.

**B5. Service sans session affinity, pas de sticky côté Ingress**
- `helm/templates/service.yaml` n'a pas `sessionAffinity: ClientIP`.
- `helm/templates/ingress.yaml` n'a aucune annotation Traefik pour sticky cookie.
- Conséquence : tout PATCH/PUT d'un upload chunked a une chance ~50% de partir sur le mauvais pod → 409. Le code suggère « configure session affinity on your load balancer » (`oci.go:548`) mais le chart ne le fait pas.

**B6. Redis = SPOF non documenté**
- `src/pkg/redis/client.go:34-37` : ping fail-fast au démarrage → si Redis down, l'app **ne démarre pas**.
- `Acquire()` retourne erreur si Redis ne répond pas → `IndexService.UpdateIndex()` (`services/index.go:87-97`) échoue après 5 retries × 200ms → upload de chart fail.
- Helm chart : aucun déploiement Redis bundled, aucune doc sur Sentinel/cluster, aucun PDB pour Redis. Si tu pointes sur un Redis single-instance, **tu remplaces 1 SPOF (oci-storage) par 1 SPOF (redis)**.

### 🟡 Importants

**I1. Probes trop agressives pour un workload IO-lourd**
- `helm/values.yaml:108-124` : `livenessProbe.timeoutSeconds: 1`, `readinessProbe.timeoutSeconds: 1`, `failureThreshold: 3`, `periodSeconds: 5`.
- Pendant un push de 5 Go (Fiber `BodyLimit: 10GB` + `WriteTimeout: 30 min` — `main.go:218-222`), le `/health` peut prendre >1s sous IO contention. 3 échecs × 5s = pod tué après 15s. Avec 2 replicas, l'autre pod hérite de tout le trafic → cascade.
- `livenessProbe.initialDelaySeconds: 2` : trop court pour le démarrage (load config + cache state + Redis ping + S3 HeadBucket).

**I2. Resources sous-dimensionnées vs. workload réel**
- `helm/values.yaml:53-58` : `requests 200m/512Mi`, `limits 1000m/2Gi`.
- Avec `largeBlobSemaphore=7` + `smallBlobSemaphore=5` = 12 streams concurrents, chacun pouvant être un blob multi-GB streamé via `io.Copy` → buffers HTTP + GC pressure → risque OOMKill à 2 Gi.
- Le sidecar Trivy partage le pod mais a son propre limit (1 Gi). En burst, le pod total peut dépasser la mémoire allouable du node.

**I3. Timeout max plafonné à 30 min pour les très gros blobs**
- `config/config.go:243-245` : `MaxTimeoutMinutes` défaut 30. Formule : 60s base + 120s/GB. Pour 15 Go : 60 + 1800 = 1860s ≈ 31 min → capé à 30 min.
- Pour des modèles ML (NVidia NGC, layers 20-40 Go) : risque de timeout en fin de download. À porter à 60-90 min via env `PROXY_TIMEOUT_MAX_MINUTES`.

**I4. GC sans lock distribué**
- `src/pkg/services/gc.go:54-60` : `gc.mu` est un mutex local + flag `running` per-pod.
- Le CronJob (`helm/templates/cronjob-gc.yaml`) tape sur `http://oci-storage.svc/gc` → kube-proxy round-robin → 1 pod fait le GC.
- TOCTOU : pendant `cleanOrphanBlobs` (gc.go:149-200), un push concurrent sur l'autre pod peut écrire un manifest référençant un blob fraîchement uploadé. Le scan de digests référencés est complété **avant** la suppression → blob neuf supprimé comme « orphan ».
- Risque de **perte de données** pendant le GC sous charge. Il faut un Redis lock global autour du `Run()`.

**I5. Index regeneration coûteux + scan complet à chaque chart**
- `services/index.go:117-167` : `UpdateIndex` lit **toutes** les `.tgz` via `backend.Read` (full bytes en mémoire) → CPU + RAM proportionnels au nombre de charts. Lock distribué OK, mais sur S3 c'est aussi du download network.
- À chaque `SaveChart` → `UpdateIndex` complet (`services/chart.go:66-69`). Pour 200 charts × 50 Mi → ~10 Gi de RAM transitoire par push. Combiné avec I2 → OOM probable.

### 🟢 Recommandés

**R1. `cacheState.json` divergent entre pods**
- `services/proxy.go:587-624` : chaque pod charge `state.json` au start, le réécrit en LWW via `go saveCacheState()`. Avec 2 pods : update perdues.
- En pratique `GetCacheState()` recalcule depuis le filesystem, donc le fichier est cosmétique. À supprimer ou migrer vers Redis (HSET `oci:cache:state`).

**R2. `loadCacheState` rate les nouveaux items après start**
- Cosmétique mais peut tromper l'UI (`/cache/status` renvoyant un total stale).

**R3. Backup non-coordonné**
- `services/backup.go` : pas de lock distribué. Si quelqu'un POST `/backup` pendant que le CronJob de l'autre pod backup → conflits cloud (overwrites partiels selon le provider).

**R4. PathManager.GetDiskStats ne s'applique qu'au PV local**
- `paths.go:94-107` : `syscall.Statfs` sur `data/`. En mode S3, ce chemin contient seulement le temp local → la stat ne reflète pas la vraie capacité storage. L'UI affiche une fausse info en mode S3.

## 4. Cause racine de tes timeouts pull large images

Trois suspects par ordre de probabilité :

1. **Pas d'anti-affinity + nodeSelector dur** → en réalité tu n'as **qu'1 pod actif** (le 2e ne peut pas démarrer si PVC RWO, ou tombe avec le node ryzen). Quand ce pod sature ses sémaphores (`5 small + 7 large`), les requêtes suivantes attendent jusqu'à `MaxTimeoutMinutes` puis renvoient 504. → **Vrai goulot : pas vraiment 2 pods.**
2. **Plafond timeout 30 min** insuffisant pour blobs >15 Go (cf I3).
3. **Probe `timeoutSeconds: 1`** sous IO load → pod sort du LB pendant qu'il sert un gros blob → client coupe avec une 502/timeout côté Ingress (cf I1).

Action de diagnostic immédiate :
```bash
kubectl get pods -n oci-storage -o wide  # combien réellement Ready, sur quels nodes ?
kubectl logs -n oci-storage -l app.kubernetes.io/component=server --tail=200 | grep -E "Timeout|semaphore|504"
kubectl top pods -n oci-storage  # vérifier OOMKills via kubectl describe pod
```

## 5. Comportement attendu vs. réel — perte d'un pod

| Scénario | Comportement actuel | Ce qu'on veut |
|---|---|---|
| Pod-A meurt, pod-B vivant, **PVC RWO + sans S3** | Pod-A bloque le PVC (ReadWriteOnce). Pod-B ne peut pas remonter. **Downtime total** jusqu'à reschedule + détach forcé du volume. | Avec S3 + multi-node : pas de PVC partagé, pod-B prend le trafic |
| Pod-A meurt en plein **chunked upload** | Le client reçoit RST. Si retry → tombe sur pod-B → 409 (UploadTracker dit que pod-A possède l'UUID). Client doit recommencer **from byte 0**. | Reprise via S3 multipart ou PVC RWX dédié `temp/` |
| Pod-A meurt en plein **proxy blob download** | Connexion client cassée. Mais le tempfile est local sur pod-A — perdu. Aucune coalescence cross-pod : si le client retry, pod-B redémarre le download à 0. | Single-flight Redis : pod-B voit la lock active, attend ou prend le relais |
| Pod-A meurt pendant **UpdateIndex** | Lock Redis expire au TTL (30s). Pod-B prend le relais à la prochaine demande. ✅ | OK |
| Node ryzen meurt | **Tout tombe** (nodeSelector + affinity vide). | Anti-affinity + suppression nodeSelector |

## 6. Comportement attendu — partage du volume

| Backend | Comportement multi-pod |
|---|---|
| Local PVC `RWO` | ❌ Un seul pod peut mounter. 2e pod = stuck `ContainerCreating`. |
| Local PVC `RWX` (NFS, Longhorn-RWX, CephFS) | ⚠️ Possible mais non testé. Le code utilise `os.Rename` pour atomicité (storage/local.go:74). Sur NFS, rename inter-process est atomique sur le même volume mais les locks fcntl peuvent diverger. Risque sur les uploads concurrents même blob. |
| **S3 (Garage/MinIO/AWS)** | ✅ Supporté. Toutes les écritures finales passent par `backend.Import` (s3.go:256-279) qui fait un PutObject. Atomicité S3 = OK. **Mais le temp staging reste local au pod** (s3.go:247-254). |

**Recommandation** : S3 obligatoire en HA. Pas de RWX en NFS pour ce workload.

## 7. Plan d'action — vers une version 2-replicas prod-ready

### Phase 1 — Quick wins (Helm chart only, pas de code)

```yaml
# values.yaml — patch HA
replicas: 2
s3:
  enabled: true
  endpoint: "http://garage.garage.svc:3900"
  bucket: "oci-storage"
  existingSecret: "garage-credentials"
redis:
  enabled: true
  addr: "redis-master.redis.svc:6379"
  existingSecret: "redis-credentials"

# Supprimer le pin sur ryzen
nodeSelector: {}

# Anti-affinity dure : pas 2 pods sur le même node
affinity:
  podAntiAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      - labelSelector:
          matchLabels:
            app.kubernetes.io/component: server
        topologyKey: kubernetes.io/hostname

# Probes plus tolérantes au workload IO
livenessProbe:
  initialDelaySeconds: 30
  periodSeconds: 30
  timeoutSeconds: 5
  failureThreshold: 6        # 3 min avant kill
readinessProbe:
  initialDelaySeconds: 10
  periodSeconds: 10
  timeoutSeconds: 3
  failureThreshold: 3

# Resources réalistes pour 12 streams concurrents
resources:
  requests:
    cpu: 500m
    memory: 1Gi
  limits:
    cpu: 2000m
    memory: 4Gi

# PDB : garantir au moins 1 pod pendant les drains
pdb:
  enabled: true
  minAvailable: 1
  # supprimer maxUnavailable

# Timeout étendu pour blobs ML 20-40 Go
env:
  - name: PROXY_TIMEOUT_MAX_MINUTES
    value: "90"
  - name: PROXY_TIMEOUT_BLOB_PER_GB
    value: "60"   # Réduire si bande passante upstream rapide

# Service : sticky pour upload chunked
# (à placer dans helm/templates/service.yaml)
# spec:
#   sessionAffinity: ClientIP
#   sessionAffinityConfig:
#     clientIP:
#       timeoutSeconds: 3600

# Ingress Traefik : sticky cookie (à ajouter dans ingress.annotations)
# traefik.ingress.kubernetes.io/affinity: "true"
# traefik.ingress.kubernetes.io/session-cookie-name: oci-upload-affinity
# traefik.ingress.kubernetes.io/proxy-body-size: "10737418240"  # 10 Gi
# traefik.ingress.kubernetes.io/proxy-read-timeout: "3600"
# traefik.ingress.kubernetes.io/proxy-send-timeout: "3600"
```

Templates à modifier :
- `helm/templates/pvc.yaml` : guard `{{- if not .Values.s3.enabled }}` + ne créer le PVC que si S3 désactivé (déjà le cas via le `range`, mais ajouter accessModes RWX si on veut tomber sur Longhorn-RWX en fallback).
- `helm/templates/service.yaml` : ajouter `sessionAffinity` conditionnel sur `.Values.replicas > 1` ou `.Values.s3.enabled`.
- `helm/templates/ingress.yaml` : ajouter les annotations sticky + body-size.
- `helm/templates/NOTES.txt` (créer) : warning si `replicas > 1 && !s3.enabled`.
- Ajouter un `helpers.tpl` validate :
  ```yaml
  {{- if and (gt (int .Values.replicas) 1) (not .Values.s3.enabled) -}}
  {{- fail "replicas > 1 requires s3.enabled=true (PVC is RWO, can't be shared)" -}}
  {{- end -}}
  ```

### Phase 2 — Code : single-flight inter-pod sur le proxy

Cible : `src/pkg/handlers/oci_proxy.go:proxyBlob`. Avant `proxyService.GetBlob(...)`, prendre un lock Redis sur `proxy-blob:<digest>` :

```go
// Pseudocode
unlock, err := h.uploadTracker.(coordination.LockManager).Acquire(
    ctx, "proxy-blob:"+digest, h.calculateBlobTimeout(0))
if err != nil {
    // Un autre pod est en train de télécharger → poll le blobPath
    return h.waitForBlobOrFallback(c, digest, blobPath)
}
defer unlock()
// re-check après lock (un autre pod a pu finir entre-temps)
if exists, _ := h.backend.Exists(blobPath); exists { ... }
// download...
```

Notes :
- Le `OCIHandler` ne reçoit aujourd'hui qu'`uploadTracker` (`coordination.UploadTracker`). Il faudrait lui passer aussi `coordination.LockManager` (le client Redis implémente déjà les deux).
- TTL du lock = `MaxTimeoutMinutes` minute. Auto-release au crash du pod owner.
- Fallback si pas de Redis : skip le lock (comportement actuel).

### Phase 3 — Code : reprise des chunked uploads

Deux options :
- **A. S3 multipart upload** comme staging :
  - `PostUpload` → `CreateMultipartUpload` → stocker `uploadID` dans Redis `upload:<uuid>:s3id`.
  - `PatchBlob` → `UploadPart` (chaque chunk = 1 part).
  - `CompleteUpload` → `CompleteMultipartUpload` puis copy vers final blob path.
  - Si pod meurt : autre pod lit `uploadID` depuis Redis et continue.
- **B. PVC RWX dédié au `data/temp/`** : monter un PV NFS/Longhorn-RWX uniquement sur `temp/`. Plus simple, moins propre. Suffisant si la volumétrie temp reste raisonnable.

Préférer A : aligné avec S3 backend, pas de nouvelle dépendance.

### Phase 4 — Code : lock distribué sur GC

Dans `services/gc.go:Run`, wrapper avec `locker.Acquire(ctx, "gc-run", 1*time.Hour)`. Si lock pris par l'autre pod → return early (`return nil, nil` est déjà le comportement quand `running=true` localement).

### Phase 5 — Redis HA

Déployer Redis avec Sentinel ou en cluster mode. Le client Go `redis/v9` supporte Sentinel via `goredis.NewFailoverClient`. Modifier `redis.NewClient` pour accepter une config Sentinel :
```yaml
redis:
  mode: sentinel  # sentinel | cluster | standalone
  sentinel:
    masterName: mymaster
    addrs: ["redis-sentinel-0:26379", "redis-sentinel-1:26379"]
```
Plus PDB sur Redis lui-même.

### Phase 6 — Validation

Tests E2E à ajouter dans `tests/`:
- `TestKillPodMidPush` : push 1 Gi via crane, kill pod-A à 50%, vérifier que pod-B accepte le retry et complète.
- `TestConcurrentProxyPullSameImage` : 2 clients pull simultanément la même image lourde via les 2 pods, vérifier 1 seul DL upstream (count via Garage metrics ou request-id).
- `TestNodeFailure` : drain le node hébergeant pod-A, vérifier que pod-B continue à servir sans timeout côté client.
- `TestGCDuringUpload` : push pendant que GC tourne sur l'autre pod, vérifier 0 perte de blob.

## 8. Annexe — Synthèse fichiers analysés

| Fichier | Pourquoi |
|---|---|
| `src/cmd/server/main.go` | Wiring Fiber, body limit 10 GB, timeouts 30 min, semaphores |
| `src/pkg/coordination/coordination.go` | Interfaces LockManager / UploadTracker / ScanTracker |
| `src/pkg/redis/client.go` | Implémentation Redis (SetNX, Eval Lua release) — solide |
| `src/pkg/handlers/oci.go` | PostUpload / PatchBlob / CompleteUpload — ownership check OK, mais reprise = 0 |
| `src/pkg/handlers/oci_proxy.go` | proxyBlob — sémaphores per-pod, pas de single-flight cross-pod |
| `src/pkg/services/proxy.go` | cacheState in-memory + state.json LWW |
| `src/pkg/services/scan.go` | ScanTracker OK, decisions.json sous lock OK |
| `src/pkg/services/index.go` | UpdateIndex avec lock Redis OK, mais lecture fullread |
| `src/pkg/services/gc.go` | mutex local seulement → race possible |
| `src/pkg/storage/{local,s3}.go` | Backends — local atomic via os.Rename, S3 via PutObject |
| `helm/values.yaml` + `templates/*.yaml` | Configuration K8s — multiple SPOFs |

---

**Prochaine étape recommandée** : appliquer la Phase 1 (Helm only) en quelques heures, redéployer, retester un push gros blob et observer si les timeouts disparaissent. C'est probablement 80% du résultat. Phases 2-3 ensuite pour la vraie résilience.
