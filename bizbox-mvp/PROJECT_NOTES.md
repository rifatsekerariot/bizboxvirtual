# BizBox MVP Projesi - Geliştirme Notları

Bu dosyada, `bizbox-mvp` projesinde bugüne kadar gerçekleştirilen tüm backend, CLI, REST API, Web Arayüzü ve Kimlik Doğrulama (Auth) geliştirmeleri özetlenmiştir.

---

## 🚀 1. Go Backend & Incus İstemcisi Yetenekleri

Incus Go SDK'sı (`github.com/lxc/incus`) kullanılarak aşağıdaki temel sanal makine (VM) / konteyner yönetim fonksiyonları geliştirilmiştir:

- **CreateVM**: Belirtilen isim, imaj (örn. `images:ubuntu/22.04`), CPU limiti (`limits.cpu`) ve RAM limiti (`limits.memory`) ile `"virtual-machine"` (veya testler için konteyner) oluşturur. Uzak imaj sunucularını (`https://images.linuxcontainers.org`) otomatik olarak çözümler.
- **StartVM**: Bir makineyi başlatır (`UpdateInstanceState` API'si ile `Action: "start"`).
- **StopVM**: Bir makineyi zorla kapatmadan, temiz bir şekilde durdurur (`Action: "stop"`, `Force: false`, `Timeout: 30`).
- **DeleteVM**: Bir makineyi kalıcı olarak siler (`DeleteInstance` API'si).
- **GetVMStatus**: Belirli bir makinenin statik (`CreatedAt`, limitler) ve dinamik (`Status`, IP Adresleri) durum bilgilerini çekip birleştirir. IP adreslerinde lokal loopback (`lo`) hariç tutulup küresel IPv4 adreslerine öncelik verilir.

---

## 💻 2. CLI (Komut Satırı) Kullanımı

Uygulama derlendiğinde (`go build -o bizbox-mvp`) komut satırından doğrudan yönetilebilir:

- **Listeleme (Varsayılan)**: `./bizbox-mvp`
  - Tüm makineleri sadeleştirilmiş JSON formatında listeler.
- **VM Oluşturma**: `./bizbox-mvp create --name <isim> --cpu <çekirdek> --ram <GB> --image <imaj>`
- **VM Başlatma**: `./bizbox-mvp start --name <isim>`
- **VM Durdurma**: `./bizbox-mvp stop --name <isim>`
- **VM Durum Sorgulama**: `./bizbox-mvp status --name <isim>`
  - Detaylı durumu temiz hizalanmış bir metin tablosu olarak yazdırır.

---

## 🔌 3. REST API Sunucusu

`./bizbox-mvp serve` komutuyla başlatılan sunucu, tüm dış ağlardan erişilebilecek şekilde `0.0.0.0:8080` adresini dinler ve şu HTTP endpoint'lerini sunar:

- **GET `/api/vms`**: Tüm makineleri JSON dizisi olarak döner (HTMX harici çağrılarda).
- **GET `/api/vms/{name}`**: Belirli bir makinenin detaylı durumunu JSON formatında döner.
- **POST `/api/vms`**: Yeni makine oluşturur. (JSON gövdesi: `name`, `image`, `cpu`, `ram`).
- **POST `/api/vms/{name}/start`**: Makineyi başlatır.
- **POST `/api/vms/{name}/stop`**: Makineyi durdurur.
- **DELETE `/api/vms/{name}`**: Makineyi siler.

---

## 🎨 4. Web Arayüzü & Dashboard (HTMX)

Sistem yöneticileri için modern, kurumsal standartlarda bir web arayüzü eklenmiştir.

### Tasarım Sistemi & Arayüz Bileşenleri
- **Renkler**: `#F7F8FA` zemin, `#FFFFFF` kartlar, `#1B4B43` (aksan koyu petrol yeşili), `#16A34A` (başarı yeşili), `#DC2626` (hata kırmızısı).
- **Tipografi**: UI metinleri için **Inter** fontu, IP/RAM/CPU gibi teknik/mono veriler için **IBM Plex Mono** fontu (Google CDN).
- **Navigasyon (Sol Sabit Menü - 240px)**: Dashboard, Sanal Makineler, Ağ, Yedekleme, Ayarlar butonları (SVG ikonlu ve Türkçe etiketli). Mobil cihazlarda (`<768px`) hamburger menüye dönüşür.
- **Üst Çubuk**: Dinamik sunucu adı (hostname) ve CSS animasyonlu yeşil durum "nabız" noktası.
- **Erişilebilirlik**: `Tab` odaklanması (`outline-offset`), modal açıkken arkadaki sayfanın kaydırılmasının engellenmesi ve `ESC` tuşuyla modalların kapatılması standartlarına uyar.

### Dashboard Dinamikleri (HTMX Polling)
- **10s Yenileme**: Arayüz 10 saniyede bir `hx-get="/api/vms"` ile sayfa yenilenmeden güncellenir.
- **Loading Skeletons**: İlk yükleme esnasında veri gelene kadar parıldayan gri yükleme şablonları gösterilir.
- **Donanım Kartları (Canlı)**: Sunucunun anlık RAM kullanım yüzdesi (`/proc/meminfo` okuyarak) ve Disk kullanım yüzdesi (`syscall.Statfs` sistem çağrısı) dinamik barlarla gösterilir.
- **İşlemler Menüsü**: Tablodaki her makinenin sağındaki 3 nokta butonundan Başlat/Durdur tetiklenir, silme işleminde çift tıklamayı önleyen onay modalı açılır.
- **Boş Durum (Empty State)**: Makine yoksa kullanıcıyı yönlendiren "İlk Sisteminizi Oluşturun" ekranı belirir.
- **Hata Yönetimi**: Incus daemon bağlantısı koptuğunda arayüzde kırmızı alarm kutusu belirir ve servis geri geldiğinde otomatik kurtarılır.

### 3 Adımlı Kurulum Sihirbazı (Wizard Modal)
1. **İsim ve Sistem Tipi**: İsim girilir ve Windows Server 2022, Windows 10/11, Ubuntu, Özel ISO kartlarından biri seçilir.
2. **Kaynak Seçimi**: Küçük / Orta / Büyük hazır şablonlar veya "Gelişmiş" tıklanarak açılan manuel CPU ve RAM alanları doldurulur.
3. **Onay & Buton Kilitleme**: Seçim özeti gösterilir. "Oluştur"a basıldığında buton devre dışı kalıp "Oluşturuluyor..." yazarak çift tıklama engellenir.
- Hata durumunda hata modal içinde gösterilir; başarı durumunda tebrik mesajı gelip 1 saniye içinde modalı kapatır ve tablo HTMX event tetikleyicisiyle anında güncellenir.

---

## 🔒 5. Kimlik Doğrulama & Oturum Yönetimi (Authentication)

Uygulamanın güvenliğini sağlamak için SQLite tabanlı, çerez bazlı bir kimlik doğrulama sistemi eklenmiştir:

- **Veritabanı Katmanı**: SQLite (`bizbox.db`) üzerinde `users` tablosu oluşturuldu. Şifreler `bcrypt` kütüphanesi kullanılarak güvenli bir şekilde tek yönlü hashlenir.
- **İlk Kurulum / Seed**: Veritabanı boşsa, ilk başlangıçta otomatik olarak `admin` / `admin` kimlik bilgileriyle bir yönetici kullanıcısı oluşturulur.
- **Giriş Sayfası (`/login`)**: Tek bir kart ortada olacak şekilde modern, minimalist, erişilebilir ve kurumsal tasarım sistemine uygun giriş ekranı tasarlandı.
- **Hata Yönetimi**: Güvenlik gereği, hatalı giriş denemelerinde kullanıcı adı veya şifrenin hangisinin yanlış olduğunu belirtmeyen jenerik `"Kullanıcı adı veya şifre hatalı"` hata mesajı verilir.
- **Oturum Yönetimi**: `map[token]User` yapısında in-memory session store oluşturuldu. Başarılı girişte istemciye `HttpOnly`, `Secure` ve `SameSite=Strict` özelliklerine sahip, 24 saat geçerli bir session çerezi (`session_token`) verilir.
- **Güvenlik Middleware'i (`AuthMiddleware`)**: `/api/vms*`, `/dashboard` (ve `/` anasayfa) gibi korumalı tüm route'lar bu middleware ile sarıldı. Geçerli oturumu olmayan HTMX istekleri otomatik olarak `/login` sayfasına yönlendirilir, API isteklerine `401 Unauthorized` dönülür.
- **Çıkış İşlemi (`/logout`)**: Sol üst çubuğa entegre edilen "Çıkış Yap" butonuyla session in-memory store'dan silinir ve tarayıcı çerezi temizlenerek kullanıcı giriş sayfasına yönlendirilir.

---

## 🧪 6. Test Adımları

Geliştirilen kimlik doğrulama sistemini test etmek için aşağıdaki adımları uygulayabilirsiniz:

### 1. Uygulamayı Derleme ve Sunucuyu Başlatma
```bash
# Projeyi derleyin
go build -o bizbox-mvp

# Sunucuyu başlatın (otomatik olarak SQLite veritabanı kurulacak ve admin kullanıcısı seed edilecektir)
./bizbox-mvp serve
```

### 2. Yetkisiz Erişim Testi
- Tarayıcınızda `http://localhost:8080/` veya `http://localhost:8080/api/vms` adresine gidin.
- Oturumunuz olmadığı için tarayıcının sizi otomatik olarak `http://localhost:8080/login` sayfasına yönlendirdiğini doğrulayın.
- Doğrudan API endpoint'ine (`/api/vms`) istek atıldığında `401 Unauthorized` yanıtı döndüğünü kontrol edin.

### 3. Giriş İşlemi Testleri
- Giriş ekranında rastgele yanlış bilgiler girerek giriş yapmayı deneyin.
- `"Kullanıcı adı veya şifre hatalı"` hata mesajının kırmızı bir kutu içerisinde gösterildiğini doğrulayın.
- Doğru bilgilerle giriş yapın (Varsayılan: Kullanıcı Adı: `admin`, Şifre: `admin`).
- Başarılı girişten sonra tarayıcınızın sizi `/` adresine yönlendirdiğini ve yönetim panelini açtığını doğrulayın.

### 4. Tarayıcı Çerezleri ve Session Testi
- Tarayıcı geliştirici araçlarını (F12) açın.
- Uygulama / Depolama (Application / Storage) sekmesinden Çerezler (Cookies) bölümüne gelin.
- `session_token` isimli çerezin ayarlandığını, `HttpOnly`, `Secure` (eğer localhost dışında veya tarayıcı izin veriyorsa) ve `SameSite=Strict` bayraklarının aktif olduğunu kontrol edin.

### 5. Çıkış Yapma Testi
- Üst çubuğun sağ tarafında bulunan yeşil "Sistem Çevrimiçi" ibaresinin yanındaki "Çıkış Yap" butonuna tıklayın.
- Oturumun sonlandırılıp tekrar `/login` sayfasına yönlendirildiğinizi ve `/` adresine tekrar girmeye çalıştığınızda yönlendirmenin sürdüğünü doğrulayın.

---

## 🔄 7. ZFS Snapshot & Yedekleme Yönetimi

bizbox-mvp projesine sanal makineler ve konteynerler için ZFS tabanlı anlık görüntü (snapshot) ve yedekleme yönetimi eklenmiştir:

- **ZFS Sarmalayıcı (os/exec)**: Go'nun `os/exec` paketi kullanılarak ZFS komutları çalıştırılır.
  - `ListSnapshots(dataset string) []Snapshot`: Belirli bir veri kümesine (dataset) ait anlık görüntüleri listeler, oluşturulma zamanı ve tür bilgisini (manuel/otomatik) ayrıştırır.
  - `CreateSnapshot(dataset string) error`: Manuel anlık görüntü (`manual_<timestamp>`) oluşturur.
  - `RollbackSnapshot(snapshotName string) error`: Sistemi belirtilen anlık görüntüye geri döndürür. Rollback esnasında VM'in çalışır durumda olması veri bütünlüğünü bozabileceği için, VM otomatik olarak durdurulur, geri yüklenir ve ardından yeniden başlatılır (VM kapalıysa kapalı kalır).
  - `DestroySnapshot(snapshotName string) error`: Bir anlık görüntüyü kalıcı olarak siler.
- **Otomatik Zamanlayıcı (Goroutine + Ticker)**: Arka planda çalışan bir goroutine, her 15 dakikada bir (çevre değişkeni `AUTO_SNAPSHOT_INTERVAL_MINUTES` ile dakika cinsinden yapılandırılabilir) tüm sistemlerin otomatik anlık görüntüsünü (`auto_<timestamp>`) alır.
- **Retention (Saklama Politikası)**: Otomatik yedekleme işlemi sırasında 48 saatten eski otomatik yedekler sistemden temizlenir. Kullanıcıların kendilerinin aldığı manuel yedekler bu temizlikten etkilenmez.

### API Endpoint'leri (Kimlik Doğrulamalı)
- **GET `/api/snapshots?vm={isim}`**: Belirli bir VM'in anlık görüntü listesini döner.
- **POST `/api/snapshots`**: Manuel anlık görüntü alımını başlatır (`{"vm": "vm-name"}`).
- **POST `/api/snapshots/{id}/rollback`**: Belirtilen anlık görüntüye geri döner. Gövdede `{"confirm": true}` onayı zorunludur.

### UI Bileşenleri (Tasarım Sistemine Uygun)
- **Sistem Detayları & Yedekler Modalı**: Dashboard üzerinde sistem ismine veya dropdown menüdeki "Yedekler & Detaylar" seçeneğine tıklandığında açılır.
- **Zaman Çizelgesi (Timeline)**: Yatay bir çizgi üzerinde yer alan interaktif noktalarla temsil edilir. Yeşil noktalar otomatik, mavi noktalar manuel yedekleri belirtir.
- **Tooltip**: Noktaların üzerine gelindiğinde yedekleme tarihi gösterilir.
- **Popover**: Noktaya tıklandığında açılan popover üzerinden doğrudan geri dönüş tetiklenir.
- **Onay Modalı**: Geri yükleme işlemi yapılmadan önce "Bu işlem [tarih]'ten sonraki tüm değişiklikleri geri alacak. Emin misiniz?" uyarısıyla onay ister.
- **İşlem Durum Göstergesi**: Geri yükleme sırasında arka plandaki VM durumu kontrol edilerek kullanıcıya "Geri dönülüyor, VM yeniden başlatılacak..." gibi net durum mesajları sunulur.

---

## 🧪 8. ZFS Snapshot Test Adımları

Geliştirilen ZFS snapshot sistemini test etmek için aşağıdaki adımları uygulayabilirsiniz:

### 1. Test Ortamı Hazırlığı (Container Oluşturma)
KVM desteği bulunmayan sanallaştırılmış ortamlarda, Incus CLI üzerinden bir test konteyneri oluşturup ZFS dataset'ini hazırlayabilirsiniz:
```bash
# Alpine imajı ile bir test konteyneri oluşturun
incus create 2c689e6efb4f testcontainer
```

### 2. Uygulamayı Derleme ve Sunucuyu Başlatma
```bash
# Projeyi derleyin
go build -o bizbox-mvp

# Sunucuyu başlatın
./bizbox-mvp serve
```

### 3. API Yetkilendirme (Oturum Açma)
API isteklerini yetkilendirmek için admin kullanıcısı ile giriş yapıp session token çerezini saklayın:
```bash
curl -i -s -c cookie.txt -d "username=admin&password=admin" http://localhost:8080/api/login
```

### 4. ZFS Snapshot API Testleri
```bash
# 1. Snapshot listesini sorgulayın (Başlangıçta boş dönecektir: [])
curl -s -b cookie.txt "http://localhost:8080/api/snapshots?vm=testcontainer"

# 2. Manuel snapshot oluşturun
curl -i -s -b cookie.txt -H "Content-Type: application/json" -d '{"vm":"testcontainer"}' http://localhost:8080/api/snapshots

# 3. ZFS üzerinde snapshot oluştuğunu doğrulayın
zfs list -t snapshot

# 4. Geri dönme (Rollback) isteği gönderin (Onaysız deneme - Hata döner)
curl -i -s -b cookie.txt -H "Content-Type: application/json" -d '{"confirm":false}' http://localhost:8080/api/snapshots/testcontainer@manual_[TIMESTAMP]/rollback

# 5. Geri dönme (Rollback) isteği gönderin (Onaylı deneme - Başarılı)
curl -i -s -b cookie.txt -H "Content-Type: application/json" -d '{"confirm":true}' http://localhost:8080/api/snapshots/testcontainer@manual_[TIMESTAMP]/rollback
```
*(Not: `[TIMESTAMP]` kısmını bir önceki sorguda listelenen `short_name` veya `id` içerisindeki değerle değiştirin.)*

### 5. Arayüz (Web UI) Testi
1. Tarayıcıda `http://localhost:8080/` adresine gidin.
2. `admin` / `admin` kimlik bilgileriyle giriş yapın.
3. Tabloda listelenen `testcontainer` ismine tıklayın veya sağdaki üç nokta menüsünden **Yedekler & Detaylar** seçeneğini seçin.
4. Açılan modalda **Yedekler (ZFS Snapshots)** sekmesine geçin.
5. **Yedek Al (Manuel)** butonuna basarak yeni bir yedek oluşturun; yatay çizgi üzerinde yeni bir mavi nokta belirdiğini görün.
6. Noktaya tıklayıp **Geri Dön** butonuna basın; açılan onay modalını onaylayarak geri yükleme işleminin tamamlanmasını ve durum bildirimlerinin gösterilmesini izleyin.

---

## 📺 9. VM Konsol & noVNC Entegrasyonu

bizbox-mvp projesine sanal makinelerin grafik arayüzlerine web tarayıcısından noVNC ile erişebilmek için konsol proxy modülü eklenmiştir:

- **Go WebSocket Proxy (`/ws/console/{vm-name}`)**: Gorilla WebSocket kullanılarak backend tarafında bir köprü kurulmuştur. Gelen WebSocket bağlantısı bir `io.ReadWriteCloser` sarmalayıcısı (`WSReadWriteCloser`) ile sarmalanarak Incus Go SDK'sının `ConsoleInstance` fonksiyonuna `Terminal` olarak aktarılır. Bu sayede tarayıcı ile Incus'un iç VGA/konsol soketi arasında çift yönlü veri akışı sağlanır.
- **Minimal noVNC Sayfası (`/console/{vm-name}`)**: noVNC kütüphanesi npm bağımlılığı olmadan doğrudan jsDelivr CDN'i üzerinden (`core/rfb.js` modülü) dinamik olarak yüklenir. Tam ekran modunda çalışan sayfada, üst kısımda VM adı ve "Bağlantıyı Kes" butonu içeren minimal bir başlık yer alır.
- **Otomatik Yeniden Bağlanma (Reconnect) Mantığı**: Bağlantı koptuğunda kullanıcıya `"Bağlantı kesildi, yeniden bağlanılıyor..."` uyarısı gösterilir ve 2 saniyelik aralıklarla 3 kez otomatik yeniden bağlanma denemesi yapılır. Başarısızlık durumunda `"Bağlanılamadı, sayfayı yenileyin"` uyarısı ekranda kalır.
- **Güvenlik ve Middleware**: Yeni eklenen `/console/{vm-name}` ve `/ws/console/{vm-name}` endpoint'leri projedeki mevcut `AuthMiddleware` kapsamına dahil edilerek `session_token` çerezi üzerinden otomatik olarak korunur. Yetkisiz istekler doğrudan `/login` sayfasına yönlendirilir.
- **Pürüzsüz Dashboard Entegrasyonu**: Dashboard tablosundaki VM satırlarının sağındaki üç nokta menüsünde bulunan "Konsola Bağlan" butonu güncellenmiştir. Bu butona tıklandığında VM konsol sayfası yeni bir tarayıcı sekmesinde (`_blank`) açılır.

---

## 🧪 10. VM Konsol Test Adımları

Geliştirilen noVNC VM konsol sistemini test etmek için aşağıdaki adımları uygulayabilirsiniz:

### 1. Uygulamayı Derleme ve Sunucuyu Başlatma
```bash
# Projeyi derleyin
go build -o bizbox-mvp

# Sunucuyu başlatın
./bizbox-mvp serve
```

### 2. Yetkilendirme Testi
- Tarayıcıda doğrudan `http://localhost:8080/console/test-vm` adresine gidin.
- Giriş yapmadıysanız, sistemin sizi otomatik olarak `http://localhost:8080/login` sayfasına yönlendirdiğini doğrulayın.

### 3. Arayüz ve Yeni Sekmede Açma Testi
1. `admin` / `admin` kimlik bilgileriyle giriş yapın.
2. Dashboard tablosundaki herhangi bir makinenin en sağındaki üç nokta ikonuna tıklayarak dropdown menüyü açın.
3. **Konsola Bağlan** seçeneğine tıklayın.
4. Konsol sayfasının yeni bir tarayıcı sekmesinde `http://localhost:8080/console/{vm-name}` URL'si ile açıldığını doğrulayın.

### 4. noVNC Bağlantı Arayüzü ve Reconnect Testi
1. Konsol sayfası açıldığında üstte minimal bir header içinde VM adının ve sağında kırmızı renkli **Bağlantıyı Kes** butonunun yer aldığını doğrulayın.
2. VM başlatılmamışsa veya ortamda KVM desteği bulunmadığı için Incus VGA soketine bağlanılamıyorsa, noVNC bağlantısının kurulamayıp sistemin otomatik olarak 3 kez yeniden bağlanmayı denediğini kontrol edin:
   - İlk aşamada: `"Bağlantı kuruluyor..."` durumunun gösterilmesi.
   - Bağlantı koptuğunda: `"Bağlantı kesildi, yeniden bağlanılıyor..."` uyarısının çıkması (2 saniye ara ile 3 defa tekrarlar).
   - 3 deneme sonunda: `"Bağlanılamadı, sayfayı yenileyin"` hata mesajının kalıcı olarak ekranda kalması.
3. Sayfayı yenilediğinizde (F5 veya Yenile) deneme sürecinin tekrar 1'den başladığını doğrulayın.
### 5. Bağlantıyı Kes Butonu Testi
1. Konsol sayfasının sağ üstündeki **Bağlantıyı Kes** butonuna tıklayın.
2. Bağlantının kapatıldığını ve ekranda `"Bağlantı kesildi."` mesajının göründüğünü, bu durumda otomatik yeniden bağlanma döngüsünün tetiklenmediğini doğrulayın.

---

## 🌐 11. OVS Ağ Segmentasyonu Yönetimi

bizbox-mvp projesine, sanal makineler ve konteynerlerin birbirleriyle olan ağ erişimlerini kısıtlamak ve organize etmek için Open vSwitch (OVS) tabanlı ağ segmentasyonu modülü eklenmiştir:

- **OVS Sarmalayıcısı (ovs-vsctl & ovs-ofctl)**: Go'nun `os/exec` kütüphanesi kullanılarak Open vSwitch CLI araçları üzerinden VLAN segmentasyonu ve güvenlik izolasyonları yönetilir.
  - `ListNetworkSegments() []Segment`: Veritabanındaki tüm segmentleri ve bu segmentlere atanmış sanal makineleri (VM) listeler.
  - `CreateSegment(name string, vlanID int) error`: Yeni bir VLAN segmenti oluşturur, veritabanına kaydeder ve OVS bridge yapılandırmalarını tetikler.
  - `AssignVMToSegment(vmName string, segmentName string) error`: Bir sanal makineyi hedef segment ile ilişkilendirir. İlgili VM'in OVS bridge üzerindeki portunun VLAN ID etiketini (tag) günceller (`ovs-vsctl set port veth-<vmName> tag=<vlanID>`).
- **Veritabanı Katmanı & Otomatik VLAN ID**: SQLite veritabanı üzerinde `network_segments` ve `network_segment_vms` tabloları ile segment bilgileri ve makine atamaları saklanır. Yeni segment eklenirken, çakışmaları önlemek için otomatik olarak sıradaki en büyük VLAN ID (`max(vlan_id) + 10`) atanır.
- **Varsayılan Segment Seeding**: Proje ilk çalıştırıldığında `"Muhasebe"` (VLAN 10), `"Misafir Wifi"` (VLAN 20) ve `"Kameralar"` (VLAN 30) isimli 3 varsayılan segment otomatik olarak seed edilir.
- **Kritik Güvenlik Mantığı (İzolasyon Kuralları)**: 
  - Open vSwitch normalde Layer 2 seviyesinde VLAN segmentasyonu yaparak portları izole eder.
  - Ancak güvenlik politikalarını sıkılaştırmak ve VLAN'lar arası yetkisiz geçişleri engellemek için `ovs-ofctl` aracılığıyla **Default-Deny** mantığında OpenFlow kuralları eklenmiştir.
  - Gelen paketler integration bridge (`br-int`) üzerinde Table 0'dan Table 1 (İzolasyon Tablosu)'e gönderilir: `ovs-ofctl add-flow br-int "priority=1000,dl_vlan=X,actions=resubmit(,1)"`.
  - İzolasyon tablosunun (Table 1) sonuna tüm inter-VLAN trafiği engelleyen düşük öncelikli bir drop kuralı eklenir: `ovs-ofctl add-flow br-int "table=1,priority=1,actions=drop"`.
  - Sadece aynı VLAN ID'sine sahip paketlerin switch üzerinde normal modda haberleşmesine izin verilir: `ovs-ofctl add-flow br-int "table=1,priority=100,dl_vlan=X,actions=normal"`.

### API Endpoint'leri (Kimlik Doğrulamalı)
- **GET `/api/network/segments`**: Segmentlerin JSON listesini döner. HTMX isteklerinde (`HX-Request: true`) doğrudan şablon motorundan render edilen HTML ağ sayfasını döner.
- **POST `/api/network/segments`**: Gönderilen segment ismini (`name`) alıp otomatik VLAN ID ile yeni segment oluşturur.
- **POST `/api/network/segments/{name}/assign`**: Gönderilen VM'i (`{"vm": "isim"}`) belirtilen segmente atar.

---

## 🧪 12. Ağ Segmentasyonu Test Adımları

Geliştirilen OVS Ağ Segmentasyonu modülünü test etmek için aşağıdaki adımları uygulayabilirsiniz:

### 1. Birim Testlerin (Unit Tests) Çalıştırılması
Yazılan API ve sarmalayıcı birim testlerini in-memory SQLite kullanarak çalıştırmak için:
```bash
go test -v -run "TestNetwork" ./...
```
Tüm testlerin başarıyla geçtiğini (PASS) doğrulayın.

### 2. Uygulamayı Derleme ve Sunucuyu Başlatma
```bash
# Projeyi derleyin
go build -o bizbox-mvp

# Sunucuyu serve sub-command'ı ile başlatın (Seed segmentler otomatik olarak yüklenecektir)
./bizbox-mvp serve
```

### 3. API Yetkilendirme (Oturum Açma)
```bash
curl -i -s -c cookie.txt -d "username=admin&password=admin" http://localhost:8080/api/login
```

### 4. REST API Üzerinden Testler
```bash
# 1. Segment listesini sorgulayın (Seed edilen 3 segmenti görmelisiniz)
curl -s -b cookie.txt http://localhost:8080/api/network/segments

# 2. Yeni segment ekleyin (Otomatik olarak VLAN 40 atanacaktır)
curl -i -s -b cookie.txt -H "Content-Type: application/json" -d '{"name":"Pazarlama"}' http://localhost:8080/api/network/segments

# 3. Bir VM'i (örn: "test-vm") "Pazarlama" segmentine atayın
curl -i -s -b cookie.txt -H "Content-Type: application/json" -d '{"vm":"test-vm"}' http://localhost:8080/api/network/segments/Pazarlama/assign

# 4. Segment listesini tekrar sorgulayarak atamanın yapıldığını ve "test-vm" isminin Pazarlama segmentinin vms dizisinde yer aldığını doğrulayın
curl -s -b cookie.txt http://localhost:8080/api/network/segments
```

### 5. Web Arayüzü (UI) Üzerinden Testler
1. Tarayıcınızdan `http://localhost:8080/` adresine gidip `admin`/`admin` bilgileriyle giriş yapın.
2. Sol menüdeki **Ağ** seçeneğine tıklayın.
3. Ekranda **Muhasebe**, **Misafir Wifi** ve **Kameralar** kartlarının listelendiğini, her kartın altında yeşil renkli **🔒 İzole** etiketinin yer aldığını doğrulayın.
4. Sağ üstteki **Yeni Segment** butonuna tıklayın, açılan modalda "Pazarlama" yazıp **Oluştur** butonuna basın. Sayfa yenilenmeden listenin güncellendiğini ve "Pazarlama (VLAN 40)" kartının eklendiğini görün.
5. Tekrar sol menüden **Dashboard**'a dönün.
6. Listedeki herhangi bir VM satırının en sağındaki üç nokta menüsüne tıklayın ve **Ağ Değiştir** seçeneğini seçin.
7. Açılan modalda yer alan dropdown listesinden "Pazarlama (VLAN 40)" seçeneğini seçip **Kaydet** butonuna basın.
8. Sol menüden tekrar **Ağ** sekmesine geçin. Seçtiğiniz sanal makinenin "Pazarlama" kartı altında listelendiğini doğrulayın.

---

## 🌐 13. Trafik Önceliklendirme (QoS) Yönetimi

bizbox-mvp projesine, sanal makineler ve ağ segmentleri için Linux `tc` (Traffic Control) komutlarını kullanan HTB (Hierarchical Token Bucket) tabanlı trafik önceliklendirme (QoS) modülü eklenmiştir:

- **Linux `tc` Sarmalayıcısı**: Go'nun `os/exec` kütüphanesi ile `tc` komutları çalıştırılır.
  - `SetPriority(segmentOrVM string, priorityLevel string) error`: Belirtilen VM veya segment için QoS kuralı atar, bunu SQLite veritabanına kaydeder ve ilgili ağ arayüzlerine (`veth-<vmName>`) anında yansıtır.
  - `ApplyQoSForVM(vmName string) error`: Bir sanal makinenin efektif öncelik seviyesini (doğrudan VM kuralı veya üye olduğu segmentten miras alınan kural) hesaplar ve HTB'yi yapılandırır.
- **Arka Plan HTB Yapılandırması & Bant Genişliği Limitleri**:
  - Geliştirme/simülasyon ortamlarında ağ arayüzü veya root yetkisi yoksa `[TC MOCK]` logları basılarak akış kesilmeden devam edilir.
  - Varsayılan bant genişliği değerleri (çevre değişkenleri ile ezilebilir):
    - **Toplam Bant Genişliği (QOS_TOTAL_BANDWIDTH):** 100 Mbps (Hat hızı).
    - **Yüksek Öncelik (QOS_HIGH_RATE):** 80 Mbps garanti bant genişliği, minimum prio 1.
    - **Normal Öncelik (QOS_NORMAL_RATE):** 20 Mbps standart garanti, prio 2.
    - **Düşük Öncelik (QOS_LOW_RATE & QOS_LOW_CEIL):** 2 Mbps garanti ve 10 Mbps maksimum hız sınırı (rate-limiting), prio 3.
- **QoS Kurallarının Miras Alınması (Inheritance)**:
  - VM'ler, doğrudan kendilerine atanmış bir öncelik kuralı yoksa üye oldukları ağ segmentinin öncelik kuralını miras alırlar.
  - VM segment değiştirdiğinde veya segmentin önceliği güncellendiğinde, arka planda çalışan `tc` yapılandırması otomatik olarak güncellenir.

### API Endpoint'leri (Kimlik Doğrulamalı)
- **GET `/api/qos/rules`**: Tanımlanmış tüm QoS kurallarının JSON listesini döner.
- **POST `/api/qos/rules`**: Belirtilen hedef (segment veya VM) için öncelik ayarlar (`{"target": "Muhasebe", "priority": "high"}`). Form data ve JSON formatlarını destekler.

### UI Bileşenleri (Ağ Sayfası Entegrasyonu)
- **Tab Arayüzü**: Ağ sayfası "Ağ Segmentasyonu" ve "Trafik Önceliği (QoS)" adında iki sekmeye ayrılmıştır. Aktif sekme bilgisi `localStorage` üzerinde saklanarak HTMX sayfa güncellemelerinde kaybolmaz.
- **Segmented Control**: Her segment ve VM için Düşük / Normal / Yüksek seçenekleri içeren şık bir yatay buton grubu (segmented control) sunulur. Seçim yapıldığı an HTMX aracılığıyla POST isteği gönderilir ve yan tarafta 2 saniye içinde sönen yeşil bir "Kaydedildi ✓" geri bildirimi belirir.

---

## 🧪 14. Trafik Önceliklendirme (QoS) Test Adımları

Geliştirilen Trafik Önceliklendirme (QoS) modülünü test etmek için aşağıdaki adımları uygulayabilirsiniz:

### 1. Birim Testlerin (Unit Tests) Çalıştırılması
QoS veritabanı, öncelik miras alma mantığı ve REST API endpoint testlerini içeren testleri çalıştırmak için:
```bash
go test -v -run "TestQoS" ./...
```
Tüm birim testlerinin başarıyla geçtiğini (PASS) doğrulayın.

### 2. Uygulamayı Derleme ve Sunucuyu Başlatma
```bash
# Projeyi derleyin
go build -o bizbox-mvp

# Sunucuyu serve sub-command'ı ile başlatın
./bizbox-mvp serve
```

### 3. API Yetkilendirme (Oturum Açma)
```bash
curl -i -s -c cookie.txt -d "username=admin&password=admin" http://localhost:8080/api/login
```

### 4. REST API Üzerinden QoS Testleri
```bash
# 1. QoS kurallarını listeleyin (Başlangıçta boş list dönecektir: [])
curl -s -b cookie.txt http://localhost:8080/api/qos/rules

# 2. "Muhasebe" segmenti için yüksek öncelik ayarlayın
curl -i -s -b cookie.txt -H "Content-Type: application/json" -d '{"target":"Muhasebe", "priority":"high"}' http://localhost:8080/api/qos/rules

# 3. "test-vm" sanal makinesi için düşük öncelik ayarlayın
curl -i -s -b cookie.txt -H "Content-Type: application/json" -d '{"target":"test-vm", "priority":"low"}' http://localhost:8080/api/qos/rules

# 4. Kuralları tekrar sorgulayarak eklenen kuralları JSON formatında görün
curl -s -b cookie.txt http://localhost:8080/api/qos/rules
```

### 5. Web Arayüzü (UI) Üzerinden QoS Testleri
1. Tarayıcınızdan `http://localhost:8080/` adresine gidip `admin`/`admin` bilgileriyle giriş yapın.
2. Sol menüdeki **Ağ** seçeneğine tıklayın.
3. Üstteki sekmelerden **Trafik Önceliği (QoS)** sekmesine geçiş yapın.
4. Ağ Segmentleri Öncelikleri tablosunda listelenen "Muhasebe", "Misafir Wifi" ve "Kameralar" segmentlerinin öncelik durumunu inceleyin.
5. "Muhasebe" segmentinin öncelik kontrolünden **Yüksek** seçeneğine tıklayın.
6. Butonun renginin anında koyu yeşile döndüğünü, tablonun en sağında yeşil renkli **Kaydedildi ✓** ifadesinin belirdiğini ve 2 saniye sonra yavaşça silindiğini doğrulayın.
7. Sayfayı yenileyin veya sol menüden Dashboard'a gidip tekrar Ağ sayfasına dönün. Sayfanın **Trafik Önceliği (QoS)** sekmesiyle açıldığını ve "Muhasebe" segmentinin "Yüksek" olarak seçili kaldığını doğrulayın.
8. Alt kısımdaki Sanal Makine Öncelikleri tablosunda listelenen aktif sanal makinelerin "Efektif Bant Genişliği" sütununda miras (inheriting) durumlarının gösterildiğini kontrol edin.
9. Bir VM'e özel doğrudan öncelik atadığınızda effective limitinin güncellendiğini ve segment miras durumunun kalktığını doğrulayın.

---

## 🛡️ 15. XDP/eBPF Tabanlı DDoS Saldırı Koruması Modülü

bizbox-mvp projesine, kernel düzeyinde paket filtreleme ve DDoS koruma durumunu simüle eden ve yöneten XDP tabanlı güvenlik paneli eklenmiştir:

- **Servis Durumu Simülasyonu**: eBPF/XDP modülünün açık/kapalı durumunu kontrol etmek amacıyla backend'de `systemctl start/stop` benzeri bir servis kontrolü simülasyonu katmanı geliştirilmiştir. Durum bilgileri SQLite üzerinde `security_settings` tablosunda saklanır.
- **Loglama & Olay Takibi**: Güvenlik modülünün açılıp kapatılması veya paket engelleme olayları `security_logs` tablosuna kaydedilir.
- **Canlı Durum/Sayaç Göstergesi**: Son 24 saatte engellenen istek sayısı arayüzde büyük fontla gösterilir.
- **Güvenlik Notu**: XDP gerçek kernel entegrasyonu YAPILMADI, mock veri kullanıldı, production öncesi ayrı bir güvenlik mühendisliği görevi gerekiyor.

### API Endpoint'leri (Kimlik Doğrulamalı)
- **GET `/api/security/status`**: Koruma durumunu ve engellenen istek sayısını döner: `{"active": true, "blocked_count": 1247}`.
- **POST `/api/security/toggle`**: Korumayı etkinleştirir/devre dışı bırakır.
- **GET `/api/security/page`**: HTMX istekleri için koruma arayüzü ve log tablosunu HTML olarak render eder.

### UI Bileşenleri
- **Güvenlik Sayfası**: Sol menüye eklenen "Güvenlik" butonu üzerinden erişilir.
- **Açma/Kapama Anahtarı (Toggle Switch)**: Korumayı anlık olarak devreye almak veya devredışı bırakmak için modern, responsive bir switch toggle.
- **Sayaç Kartı**: Son 24 saatte engellenen paket sayısını büyük sayılarla gösteren modern dashboard kartı.
- **Olay Tablosu**: Engellenen IP adresleri ve zaman damgalarını barındıran basit log tablosu.

---

## 🧪 16. XDP Saldırı Koruması Test Adımları

Geliştirilen XDP Saldırı Koruması modülünü test etmek için aşağıdaki adımları uygulayabilirsiniz:

### 1. Birim Testlerin (Unit Tests) Çalıştırılması
Yazılan güvenlik birim testlerini çalıştırmak için:
```bash
go test -v -run "TestSecurity" ./...
```
Tüm testlerin başarıyla geçtiğini (PASS) doğrulayın.

### 2. Uygulamayı Derleme ve Sunucuyu Başlatma
```bash
# Projeyi derleyin
go build -o bizbox-mvp

# Sunucuyu başlatın
./bizbox-mvp serve
```

### 3. API Yetkilendirme (Oturum Açma)
```bash
curl -i -s -c cookie.txt -d "username=admin&password=admin" http://localhost:8080/api/login
```

### 4. REST API Üzerinden Testler
```bash
# 1. Koruma durumunu sorgulayın
curl -s -b cookie.txt http://localhost:8080/api/security/status

# 2. Durumu değiştirin (toggle)
curl -i -s -b cookie.txt -X POST http://localhost:8080/api/security/toggle
```

### 5. Web Arayüzü (UI) Üzerinden Testler
1. Tarayıcınızdan `http://localhost:8080/` adresine gidip `admin`/`admin` bilgileriyle giriş yapın.
2. Sol menüdeki **Güvenlik** seçeneğine tıklayın.
3. Ekranda **Koruma Durumu** switch anahtarını ve **Engellenen İstekler** sayacını görün.
4. Switch anahtarına tıklayarak koruma durumunu değiştirin. Koruma durumu değiştiğinde "Durum" etiketinin (ETKİN/DEVRE DIŞI) anında güncellendiğini ve log tablosuna yeni bir işlem satırı eklendiğini doğrulayın.

---

## ⚙️ 17. Kullanıcı Ayarları Modülü (Şifre, Oturum Süresi, 2FA)

bizbox-mvp projesine, kullanıcıların güvenlik ve oturum tercihlerini yönetebilecekleri "Kullanıcı Ayarları" modülü entegre edilmiştir:

- **Şifre Değiştirme (`POST /api/settings/password`)**: Kullanıcının şifresini güvenli bir şekilde değiştirmesini sağlar. Değişiklik için mevcut şifrenin girilmesi ve doğrulanması (bcrypt hash kontrolü) zorunludur. Yeni şifreler bcrypt ile hashlenerek güncellenir.
- **Oturum Zaman Aşımı Ayarı (`POST /api/settings/session`)**: Kullanıcı oturumunun geçerlilik süresini `15m`, `30m`, `1h`, `4h` veya `24h` (varsayılan) olarak ayarlayabilir. Bu ayar veritabanına kaydedilir ve kullanıcının aktif oturum süresi (session store ve session cookie) anında seçilen süreye güncellenir.
- **TOTP Tabanlı İki Adımlı Doğrulama (2FA)**: `github.com/pquerna/otp` kütüphanesi kullanılarak standart Google Authenticator/Authy uyumlu TOTP altyapısı kurulmuştur.
  - **Kurulum/Etkinleştirme (`POST /api/settings/2fa/enable`)**: 2FA aktif değilse, backend dinamik olarak yeni bir TOTP gizli anahtarı üretir ve QR kodunu `base64` formatında doğrudan Settings sayfasında gösterir. Kullanıcı doğrulayıcı uygulamasına bu QR kodunu okutup ürettiği 6 haneli doğrulama kodunu girerek 2FA'yı etkinleştirir.
  - **Devre Dışı Bırakma (`POST /api/settings/2fa/disable`)**: Güvenlik gereği 2FA devredışı bırakılırken güncel doğrulama kodunun girilmesi zorunludur.
  - **Giriş Entegrasyonu (`POST /api/login`)**: Kullanıcının 2FA özelliği aktifse, şifre doğrulaması başarılı olduktan sonra tarayıcıya dinamik olarak 2FA kod giriş ekranı (kullanıcı adı/şifre alanları gizlenerek) sunulur ve kod doğrulanmadan oturum açılması engellenir.

### API Endpoint'leri (Kimlik Doğrulamalı)
- **GET `/api/settings/page`**: HTMX istekleri için kullanıcı ayarları sayfasını HTML olarak render eder.
- **POST `/api/settings/password`**: Şifre güncelleme isteği alır (form data).
- **POST `/api/settings/session`**: Oturum süresi güncelleme isteği alır.
- **POST `/api/settings/2fa/enable`**: 2FA'yı doğrulamalı olarak etkinleştirir.
- **POST `/api/settings/2fa/disable`**: 2FA'yı doğrulamalı olarak devredışı bırakır.

### UI Bileşenleri
- **Ayarlar Sayfası**: Sol menüye eklenen **Ayarlar** butonu üzerinden yüklenir.
- **Şifre Değiştir Formu**: Mevcut şifre, yeni şifre ve onay alanlarını barındıran kart bileşeni.
- **Oturum Süresi Formu**: Süre seçenekleri (15dk, 30dk, 1sa, 4sa, 24sa) dropdown listesi.
- **İki Adımlı Doğrulama (2FA) Kartı**: 2FA durumuna göre dinamik olarak QR kodlu kurulum alanına veya 2FA aktif durum kartına dönüşür.

---

## 🧪 18. Kullanıcı Ayarları Modülü Test Adımları

Geliştirilen Kullanıcı Ayarları modülünü test etmek için aşağıdaki adımları uygulayabilirsiniz:

### 1. Birim Testlerin (Unit Tests) Çalıştırılması
Ayarlar modülü için hazırlanan özel birim testlerini (veritabanı migrasyonu, şifre güncelleme, oturum süresi ve 2FA etkinleştirme/devredışı bırakma akışları) çalıştırmak için:
```bash
go test -v -run "TestSettings" ./...
```
Tüm testlerin başarıyla geçtiğini (PASS) doğrulayın.

### 2. Uygulamayı Derleme ve Sunucuyu Başlatma
```bash
# Projeyi derleyin
go build -o bizbox-mvp

# Sunucuyu başlatın
./bizbox-mvp serve
```

### 3. Web Arayüzü Üzerinden Şifre Değiştirme Testi
1. Tarayıcınızdan `http://localhost:8080/` adresine gidip `admin`/`admin` bilgileriyle giriş yapın.
2. Sol menüden **Ayarlar** seçeneğine tıklayın.
3. **Şifre Değiştir** formunda mevcut şifreye `admin`, yeni şifreye `newpass` yazıp kaydedin. Yeşil renkli başarı bildirimini görün.
4. Çıkış yapıp yeni şifrenizle giriş yapabildiğinizi doğrulayın.

### 4. Oturum Süresi Ayarı Testi
1. **Ayarlar** sayfasındaki **Oturum Süresi** dropdown listesinden "15 Dakika" seçip **Kaydet** butonuna basın.
2. Başarı bildirimini doğrulayın. Veritabanındaki `session_timeout` kolonunun güncellendiğini kontrol edin.

### 5. İki Adımlı Doğrulama (2FA) Testi
1. **Ayarlar** sayfasındaki **İki Adımlı Doğrulama** kartında yer alan QR kodunu telefonunuzdaki doğrulayıcı uygulama (Google Authenticator vb.) ile taratın.
2. Uygulamanın ürettiği 6 haneli kodu doğrulama kodu alanına girip **Etkinleştir ve Kaydet** butonuna basın.
3. Sayfanın yenilenip kartın yeşil renkli **✓ İki Adımlı Doğrulama Aktif** durumuna geçtiğini görün.
4. Siteden **Çıkış Yap** butonuyla çıkın.
5. Tekrar giriş yapmayı deneyin: Kullanıcı adı ve şifreyi girip "Giriş Yap" butonuna bastığınızda, sistemin şifreyi doğrulayıp dinamik olarak 2FA kod giriş ekranını getirdiğini doğrulayın.
6. Doğrulayıcı uygulamanızdaki güncel 2FA kodunu girerek sisteme giriş yapın.
7. Tekrar **Ayarlar** sayfasına gidip, 2FA kodunuzu girerek 2FA'yı devre dışı bırakın ve sistemin eski haline döndüğünü doğrulayın.

---

## ⚙️ 19. Sistem Güncelleme Modülü (ZFS Snapshot ve Rollback)

bizbox-mvp projesine, sistemin sürüm takibini yapan ve güvenli bir güncelleme akışı (rollback mekanizmalı) sunan "Sistem Güncelleme" modülü entegre edilmiştir:

- **Sürüm Kontrolü & Yapılandırma (`config/version.json`)**: Mevcut sürüm, yeni sürüm, güncelleme varlığı (`has_update`) ve sürüm notları (`changelog`) bu dosyada saklanır. Sistem açılışta ve arayüzde bu bilgileri okuyarak güncellik durumunu belirler.
- **Güvenli Güncelleme Akışı (ZFS Snapshot & Rollback)**:
  - Güncelleme işlemi başlatıldığında, sistem önce `CreateSnapshot` fonksiyonunu kullanarak mevcut sistem durumunun (`rft/bizbox` dataset'i üzerinde) anlık görüntüsünü alır.
  - Güncelleme adımları simüle edilir (dosyaların indirilmesi, kurulması).
  - Eğer kurulum esnasında bir hata oluşursa (arayüzden test amaçlı tetiklenebilir), `RollbackSnapshot` çağrılarak sistem güvenli bir şekilde güncelleme öncesi snapshot durumuna geri döndürülür ve geçici snapshot temizlenir.
  - Güncelleme başarılı olursa geçici snapshot silinir ve `config/version.json` dosyasındaki `current_version` değeri `new_version` ile güncellenir.
- **ZFS Geliştirme Ortamı Uyumluluğu (Mocking)**: Local geliştirme ve test ortamlarında ZFS veya ilgili dataset'in kurulu olmadığı durumları tolere etmek amacıyla, ZFS komutlarının bulunmadığı veya dataset'in var olmadığı koşullarda otomatik olarak `[ZFS MOCK]` logları basılarak akışın sorunsuz devam etmesi sağlanmıştır.

### API Endpoint'leri (Kimlik Doğrulamalı)
- **GET `/api/updates/check`**: Güncel sürüm detaylarını ve güncelleme durumunu JSON olarak döner.
- **POST `/api/updates/start`**: Güncelleme işlemini başlatır. Hata simülasyonu için `simulate_error=true` form parametresini kabul eder.
- **GET `/api/updates/status`**: Güncellemenin anlık ilerleme durumunu ve detaylı mesajlarını HTMX uyumlu HTML fragment'ı olarak döner.
- **POST `/api/updates/reset`**: Güncelleme durumunu sıfırlayarak sistemi varsayılan durumuna getirir.

### UI Bileşenleri (Ayarlar Sayfası)
- **Sistem Güncelleme Kartı**: Kullanıcı Ayarlar sayfasında 4. bir kart olarak sunulur.
- **Sürüm Rozeti**: Yeni güncelleme varsa yeşil renkli **Yeni Sürüm Var** rozeti gösterilir.
- **Progress Bar & Durum**: Güncelleme sırasında HTMX Polling (`hx-trigger="every 1s"`) kullanılarak parıldayan modern bir progress bar, anlık yüzde bilgisi, döner yükleme simgesi (spinner) ve adım açıklamaları dinamik olarak gösterilir.
- **Hata Simülasyonu Seçeneği**: Rollback akışını canlı test etmek amacıyla arayüze "Hata Simüle Et (Rollback Testi)" seçeneği eklenmiştir.

### Test Adımları
Geliştirilen Sistem Güncelleme modülü ve rollback mekanizmasını test etmek için:
```bash
go test -v -run TestUpdatesModule ./...
```
Tüm adımların (başarı ve hata/rollback simülasyonları) yeşil olarak geçtiğini doğrulayın.


