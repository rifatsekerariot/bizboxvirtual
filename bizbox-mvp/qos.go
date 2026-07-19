package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

var (
	// QoS Bandwidth settings (configurable via environment variables or default constants)
	// QosTotalBandwidth: Toplam fiziksel/mantıksal hat hızı (Varsayılan: 100 Mbps)
	// Bu değer, HTB sınıf hiyerarşisinin kök (root) bant genişliği sınırıdır.
	QosTotalBandwidth = 100

	// QosHighRate: "high" öncelikli trafiğe garanti edilen bant genişliği (Varsayılan: 80 Mbps)
	// High öncelikli VM/segmentlerin yoğun zamanda garanti olarak kullanabileceği hızdır.
	QosHighRate       = 80

	// QosNormalRate: "normal" öncelikli trafiğe garanti edilen bant genişliği (Varsayılan: 20 Mbps)
	// Standart öncelikli VM/segmentler için garanti edilen hızdır.
	QosNormalRate     = 20

	// QosLowRate: "low" öncelikli trafiğe garanti edilen bant genişliği (Varsayılan: 2 Mbps)
	// Düşük öncelikli VM/segmentler için yoğun zamanda garanti edilen minimum hızdır.
	QosLowRate        = 2

	// QosLowCeil: "low" öncelikli trafiğin aşamayacağı limit bant genişliği (Varsayılan: 10 Mbps)
	// Düşük öncelikli VM/segmentlerin hat boş olsa dahi çıkabileceği maksimum hız sınırıdır (Rate-limiting).
	QosLowCeil        = 10
)

func initQosConfig() {
	if val, ok := getEnvInt("QOS_TOTAL_BANDWIDTH"); ok {
		QosTotalBandwidth = val
	}
	if val, ok := getEnvInt("QOS_HIGH_RATE"); ok {
		QosHighRate = val
	}
	if val, ok := getEnvInt("QOS_NORMAL_RATE"); ok {
		QosNormalRate = val
	}
	if val, ok := getEnvInt("QOS_LOW_RATE"); ok {
		QosLowRate = val
	}
	if val, ok := getEnvInt("QOS_LOW_CEIL"); ok {
		QosLowCeil = val
	}
}

func getEnvInt(key string) (int, bool) {
	if valStr := os.Getenv(key); valStr != "" {
		var val int
		if _, err := fmt.Sscanf(valStr, "%d", &val); err == nil {
			return val, true
		}
	}
	return 0, false
}

// InitQosDB initializes the qos_rules table in the database and loads configs.
func InitQosDB() {
	initQosConfig()

	query := `
	CREATE TABLE IF NOT EXISTS qos_rules (
		target TEXT PRIMARY KEY,
		priority TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	_, err := db.Exec(query)
	if err != nil {
		log.Fatalf("qos_rules tablosu oluşturulurken hata: %v", err)
	}
	log.Println("[QoS] qos_rules tablosu başarıyla doğrulandı/oluşturuldu.")
}

// SetPriority wraps tc commands and saves QoS rules.
func SetPriority(segmentOrVM string, priorityLevel string) error {
	priorityLevel = strings.ToLower(strings.TrimSpace(priorityLevel))
	if priorityLevel != "high" && priorityLevel != "normal" && priorityLevel != "low" {
		return fmt.Errorf("geçersiz öncelik seviyesi: %s. Sadece 'high', 'normal' veya 'low' kabul edilir", priorityLevel)
	}

	_, err := db.Exec(`
		INSERT INTO qos_rules (target, priority)
		VALUES (?, ?)
		ON CONFLICT(target) DO UPDATE SET priority = excluded.priority
	`, segmentOrVM, priorityLevel)
	if err != nil {
		return fmt.Errorf("QoS kuralı kaydedilirken hata: %w", err)
	}

	// 1. Check if the target is a segment
	var segmentCount int
	err = db.QueryRow("SELECT COUNT(*) FROM network_segments WHERE name = ?", segmentOrVM).Scan(&segmentCount)
	if err == nil && segmentCount > 0 {
		// It is a segment. Find all VMs assigned to this segment and apply QoS.
		rows, err := db.Query("SELECT vm_name FROM network_segment_vms WHERE segment_name = ?", segmentOrVM)
		if err != nil {
			return fmt.Errorf("segmente ait VM'ler listelenirken hata: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var vmName string
			if err := rows.Scan(&vmName); err == nil {
				_ = ApplyQoSForVM(vmName)
			}
		}
		return nil
	}

	// 2. Otherwise assume it's a VM
	return ApplyQoSForVM(segmentOrVM)
}

// ApplyQoSForVM computes the effective priority for a VM and configures HTB.
func ApplyQoSForVM(vmName string) error {
	var priority string

	// Step a: Check if VM has a direct QoS rule
	err := db.QueryRow("SELECT priority FROM qos_rules WHERE target = ?", vmName).Scan(&priority)
	if err != nil {
		// Step b: Check if VM belongs to a segment and inherit its QoS rule
		var segmentName string
		errSeg := db.QueryRow("SELECT segment_name FROM network_segment_vms WHERE vm_name = ?", vmName).Scan(&segmentName)
		if errSeg == nil && segmentName != "" {
			errSegQoS := db.QueryRow("SELECT priority FROM qos_rules WHERE target = ?", segmentName).Scan(&priority)
			if errSegQoS != nil {
				priority = "normal" // default if segment has no rule
			}
		} else {
			priority = "normal" // default if VM has no segment
		}
	}

	interfaceName := fmt.Sprintf("veth-%s", vmName)
	return configureHTB(interfaceName, priority)
}

func configureHTB(interfaceName string, priorityLevel string) error {
	log.Printf("[QoS] HTB qdisc yapılandırılıyor: Arayüz: %s, Öncelik: %s", interfaceName, priorityLevel)

	// Determine tc class parameters
	var rate, ceil, prio string
	switch priorityLevel {
	case "high":
		rate = fmt.Sprintf("%dmbit", QosHighRate)
		ceil = fmt.Sprintf("%dmbit", QosTotalBandwidth)
		prio = "1"
	case "low":
		rate = fmt.Sprintf("%dmbit", QosLowRate)
		ceil = fmt.Sprintf("%dmbit", QosLowCeil)
		prio = "3"
	default: // "normal"
		rate = fmt.Sprintf("%dmbit", QosNormalRate)
		ceil = fmt.Sprintf("%dmbit", QosTotalBandwidth)
		prio = "2"
	}

	// 1. Delete existing root qdisc
	cmdDel := exec.Command("tc", "qdisc", "del", "dev", interfaceName, "root")
	_ = cmdDel.Run() // Ignore error if no qdisc exists

	// 2. Add htb root qdisc
	cmdAddRoot := exec.Command("tc", "qdisc", "add", "dev", interfaceName, "root", "handle", "1:", "htb", "default", "12")
	if out, err := cmdAddRoot.CombinedOutput(); err != nil {
		return fmt.Errorf("HTB root qdisc eklenemedi: %w. Detay: %s", err, string(out))
	}

	// 3. Add root class
	cmdAddClass := exec.Command("tc", "class", "add", "dev", interfaceName, "parent", "1:", "classid", "1:1", "htb", "rate", fmt.Sprintf("%dmbit", QosTotalBandwidth))
	if out, err := cmdAddClass.CombinedOutput(); err != nil {
		return fmt.Errorf("root sınıf eklenemedi: %w. Detay: %s", err, string(out))
	}

	// 4. Add default class 1:12 with the specified priority parameters
	cmdAddDefaultClass := exec.Command("tc", "class", "add", "dev", interfaceName, "parent", "1:1", "classid", "1:12", "htb", "rate", rate, "ceil", ceil, "prio", prio)
	if out, err := cmdAddDefaultClass.CombinedOutput(); err != nil {
		return fmt.Errorf("öncelikli sınıf eklenemedi: %w. Detay: %s", err, string(out))
	}

	log.Printf("[QoS] '%s' arayüzünde HTB öncelik sınıfı (rate: %s, ceil: %s, prio: %s) başarıyla yapılandırıldı.", interfaceName, rate, ceil, prio)
	return nil
}

// GetQoSRulesMap returns a map of target -> priority from database.
func GetQoSRulesMap() map[string]string {
	rows, err := db.Query("SELECT target, priority FROM qos_rules")
	if err != nil {
		log.Printf("[QoS] Kurallar alınırken hata: %v", err)
		return map[string]string{}
	}
	defer rows.Close()

	rules := make(map[string]string)
	for rows.Next() {
		var target, priority string
		if err := rows.Scan(&target, &priority); err == nil {
			rules[target] = priority
		}
	}
	return rules
}

// GET /api/qos/rules
func handleGetQoSRules(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT target, priority FROM qos_rules")
	if err != nil {
		http.Error(w, fmt.Sprintf("QoS kuralları sorgulanırken hata: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type QoSRule struct {
		Target   string `json:"target"`
		Priority string `json:"priority"`
	}

	rules := []QoSRule{}
	for rows.Next() {
		var rule QoSRule
		if err := rows.Scan(&rule.Target, &rule.Priority); err == nil {
			rules = append(rules, rule)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(rules)
}

// POST /api/qos/rules
func handleCreateQoSRule(w http.ResponseWriter, r *http.Request) {
	var target, priority string

	if r.Header.Get("HX-Request") == "true" || strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		target = r.FormValue("target")
		priority = r.FormValue("priority")
	} else {
		var req struct {
			Target   string `json:"target"`
			Priority string `json:"priority"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Geçersiz JSON içeriği", http.StatusBadRequest)
			return
		}
		target = req.Target
		priority = req.Priority
	}

	target = strings.TrimSpace(target)
	priority = strings.TrimSpace(priority)

	if target == "" || priority == "" {
		http.Error(w, "target ve priority alanları zorunludur", http.StatusBadRequest)
		return
	}

	err := SetPriority(target, priority)
	if err != nil {
		http.Error(w, fmt.Sprintf("QoS kuralı uygulanamadı: %v", err), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<span class="qos-saved-feedback" style="color: var(--success-color); font-size: 12px; margin-left: 8px; font-weight: 600; display: inline-flex; align-items: center; gap: 4px; animation: qosFadeOut 2.5s forwards;">Kaydedildi ✓</span>`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"target":   target,
		"priority": priority,
	})
}
