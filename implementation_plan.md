# Datastore Yönetimi ve Yedekleme (Snapshot) Loglama Planı

Hedeflerimiz:
1. **Yedekleme Logları ve İşlem Süreleri (Audit Trail):** Sanal makinelerde alınan yedeklerin ve geri yükleme işlemlerinin (rollback) başarılı/başarısız durumlarını, ne zaman yapıldıklarını ve işlemin **ne kadar sürdüğünü** gösterecek bir log sistemi kurmak.
2. **Datastore (Depolama Havuzu) Yönetimi:** Sistemin kurulum aşamasında otomatik `rft` ZFS pool'u kurması yerine, arayüz üzerinden boş (ham) disklerin listelenmesi ve kullanıcının seçtiği diskin ZFS olarak formatlanıp Datastore olarak Incus'a eklenmesi altyapısını kurmak.
3. **Arayüz (Sekme) Sorunlarının Kesin Çözümü:** HTMX ve önbellek (cache) kaynaklı devam eden "ağ sekmeleri ve detay sekmeleri tıklanmıyor" sorununu %100 çözecek, inline `onclick` yapısından bağımsız Native JavaScript Event Listener mimarisine geçiş yapmak.

## User Review Required
> [!IMPORTANT]
> **Datastore Seçimi ve Disk Formatlama**
> Sisteme eklenecek yeni disklerin (Örn: `/dev/sdb`, `/dev/nvme0n1`) ZFS havuzu olarak formatlanması diskteki **tüm verileri geri döndürülemez şekilde silecektir.** Arayüzde yanlışlıkla sistem diskinin (`/dev/sda` vb.) seçilip formatlanmasını engellemek için sadece "bağlı olmayan (unmounted) ve boş" diskleri listeleyeceğiz. Bu mantık sizin için uygun mudur?
> 
> Lütfen "Proceed" (Devam Et) butonuna basarak planı onaylayın.

## Önerilen Değişiklikler (Proposed Changes)

### 1. Yedekleme ve Geri Yükleme Logları (Backup Logs)
Sistemdeki `system_logs` tablosunu kullanarak veya yeni bir `backup_logs` tablosu oluşturarak geri yükleme işlemlerinin kaydını tutacağız.

#### [MODIFY] [snapshot.go](file:///d:/Antigravity/bizboxvirtual/bizbox-mvp/snapshot.go)
- `handleRollbackSnapshotAPI` ve `handleCreateSnapshotAPI` fonksiyonlarına **zaman ölçer (timer)** eklenecek.
- İşlem tamamlandığında `LogSystemEvent` fonksiyonu çağrılırken işlem süresi de rapora eklenecek (Örn: `"Başarılı (4.2 sn)"`).

#### [MODIFY] [templates/vm-detail.html](file:///d:/Antigravity/bizboxvirtual/bizbox-mvp/templates/vm-detail.html)
- "Yedekler (ZFS Snapshots)" sekmesinin altına **İşlem Geçmişi (Loglar)** bölümü eklenecek.
- Sadece o makineye (VM) ait yedek alma ve geri dönme işlemlerinin geçmişi listelenecek.

### 2. Datastore (Depolama) Yönetimi Modülü
Kullanıcının boş diskleri ZFS pool olarak sisteme dahil edebileceği yeni bir sayfa (Depolama / Storage) oluşturacağız.

#### [NEW] [storage.go](file:///d:/Antigravity/bizboxvirtual/bizbox-mvp/storage.go)
- `ListRawDisks()`: Sistemdeki `lsblk -J -d -n -o NAME,SIZE,TYPE,MOUNTPOINT` komutu ile boş diskleri bulup listeleyecek.
- `CreateDatastore(diskPath, poolName)`: Seçilen diske `zpool create <isim> <disk>` komutunu çalıştıracak ve ardından `incus admin storage create <isim> zfs source=<isim>` ile Incus'a bağlayacak.

#### [NEW] [templates/storage.html](file:///d:/Antigravity/bizboxvirtual/bizbox-mvp/templates/storage.html)
- Sisteme takılı olan fiziksel diskleri listeleyen ve Datastore oluşturmayı sağlayan arayüz dosyası eklenecek.

#### [MODIFY] [main.go](file:///d:/Antigravity/bizboxvirtual/bizbox-mvp/main.go)
- `/api/storage` ve `/api/storage/create` endpoint'leri eklenecek.

### 3. Arayüz JS Sorunlarının Kesin Çözümü (HTMX UI Fixes)
Kullanıcının tarayıcısında `onclick` fonksiyonlarının kaybolması sorununu tamamen aşmak için:

#### [MODIFY] [static/app.js](file:///d:/Antigravity/bizboxvirtual/bizbox-mvp/static/app.js)
- Inline `onclick="window.switchNetworkTab(...)"` kodlarını HTML'den tamamen kaldırıp, `app.js` içerisinde Event Delegation (olay devretme) yöntemi kullanacağız.
- `document.addEventListener('click', function(e) { if(e.target.matches('.tab-btn')) { ... } })` mantığı kurularak HTMX sonradan HTML getirse bile JS'in kopması imkansız hale getirilecek.

## Verification Plan (Doğrulama)
- Sistemdeki boş diskleri doğru listeleyip listelemediği `lsblk` komutlarıyla teyit edilecek.
- Geri yükleme yapıldığında sürenin (örn: 3.5 sn) log ekranında görünüp görünmediği test edilecek.
- Sekme geçişleri tarayıcı önbelleğinden bağımsız çalışacak şekilde konsol hataları kontrol edilecek.
