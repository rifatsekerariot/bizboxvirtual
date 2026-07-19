function toggleSidebar() {
  const sidebar = document.getElementById('sidebar');
  const backdrop = document.getElementById('sidebar-backdrop');
  if (sidebar && backdrop) {
    sidebar.classList.toggle('open');
    backdrop.classList.toggle('show');
  }
}

function toggleDropdown(event, id) {
  event.stopPropagation();
  document.querySelectorAll('.dropdown-menu').forEach(el => {
    if (el.id !== id) {
      el.classList.remove('show');
    }
  });
  const el = document.getElementById(id);
  if (el) {
    el.classList.toggle('show');
  }
}

// Close dropdowns on document click
document.addEventListener('click', () => {
  document.querySelectorAll('.dropdown-menu').forEach(el => {
    el.classList.remove('show');
  });
});

function handleInstanceAction(name, action) {
  fetch(`/api/vms/${name}/${action}`, { method: 'POST' })
    .then(resp => {
      if (!resp.ok) {
        return resp.text().then(txt => { throw new Error(txt); });
      }
      refreshCurrentTab();
    })
    .catch(err => {
      alert("İşlem gerçekleştirilemedi: " + err.message);
    });
}

function connectConsole(name) {
  window.open(`/console/${name}`, '_blank');
}

let activeDeleteVM = null;

function openDeleteModal(name) {
  activeDeleteVM = name;
  const nameEl = document.getElementById('delete-vm-name');
  if (nameEl) nameEl.innerText = name;
  const modal = document.getElementById('delete-modal');
  if (modal) modal.classList.add('show');
  document.body.style.overflow = 'hidden';
  setTimeout(() => {
    const cancelBtn = modal.querySelector('.btn-secondary');
    if (cancelBtn) cancelBtn.focus();
  }, 100);
}

function closeDeleteModal() {
  activeDeleteVM = null;
  const modal = document.getElementById('delete-modal');
  if (modal) modal.classList.remove('show');
  document.body.style.overflow = '';
}

function confirmDelete() {
  if (!activeDeleteVM) return;
  
  fetch(`/api/vms/${activeDeleteVM}`, { method: 'DELETE' })
    .then(resp => {
      if (!resp.ok) {
        return resp.text().then(txt => { throw new Error(txt); });
      }
      closeDeleteModal();
      refreshCurrentTab();
    })
    .catch(err => {
      alert("Silme işlemi başarısız: " + err.message);
      closeDeleteModal();
    });
}

// Wizard modal control
function openWizardModal() {
  const modal = document.getElementById('wizard-modal');
  if (modal) {
    modal.classList.add('show');
    document.body.style.overflow = 'hidden';
    setTimeout(() => {
      const firstInput = modal.querySelector('input, button, select');
      if (firstInput) firstInput.focus();
    }, 150);
  }
}

function closeWizardModal() {
  const modal = document.getElementById('wizard-modal');
  if (modal) {
    modal.classList.remove('show');
    document.body.style.overflow = '';
  }
}

function openCreateModal() {
  htmx.ajax('GET', '/api/wizard/step1', '#wizard-modal-body');
  openWizardModal();
}

// Keyboard accessibility
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') {
    closeWizardModal();
    closeDeleteModal();
    closeSegmentModal();
    closeNetworkModal();
    closeVMDetailModal();
  }
});

// OS selection card logic
function selectOS(card, val) {
  document.querySelectorAll('.os-card').forEach(el => el.classList.remove('active'));
  card.classList.add('active');
  document.getElementById('selected-image').value = val;
}

// Resource preset card selection
function selectPreset(card, preset, cpu, ram) {
  document.querySelectorAll('.preset-card').forEach(el => el.classList.remove('active'));
  card.classList.add('active');
  document.getElementById('selected-preset').value = preset;
  
  document.getElementById('custom-cpu').value = cpu;
  document.getElementById('custom-ram').value = ram;
}

function toggleAdvanced(event) {
  event.preventDefault();
  const el = document.getElementById('advanced-fields');
  if (el) {
    el.classList.toggle('show');
  }
}

function disableSubmitButton(form) {
  const btn = document.getElementById('create-vm-submit-btn');
  if (btn) {
    btn.disabled = true;
    btn.innerText = 'Oluşturuluyor...';
  }
}

/* === NEW SEGMENTATION AND VM DETAILS SCRIPTS === */

// Active sidebar state switcher
function setActiveMenu(id) {
  document.querySelectorAll('.sidebar-menu .menu-item').forEach(el => {
    el.classList.remove('active');
  });
  const el = document.getElementById(id);
  if (el) {
    el.classList.add('active');
  }
}

// VM Details modal (migrated from templates/dashboard.html)
function openVMDetail(event, name) {
  if (event) event.preventDefault();
  let modal = document.getElementById('vm-detail-modal');
  if (!modal) {
    modal = document.createElement('div');
    modal.id = 'vm-detail-modal';
    modal.className = 'modal-overlay';
    modal.innerHTML = `
      <div class="modal-content" style="max-width: 800px; width: 90%; max-height: 90vh; display: flex; flex-direction: column; overflow: hidden;">
        <div class="modal-header" style="flex-shrink: 0;">
          <h3 id="vm-detail-title" style="margin: 0; font-family: 'Inter', sans-serif;">Sistem Detayları</h3>
          <button class="modal-close" onclick="closeVMDetailModal()">&times;</button>
        </div>
        <div id="vm-detail-modal-body" class="modal-body" style="overflow-y: auto; flex-grow: 1; padding-top: 10px;">
          <div style="display:flex; justify-content:center; padding: 40px 0;">
            <div class="spinner-loader" style="width:30px; height:30px; border:3px solid #E2E8F0; border-top-color:#1B4B43; border-radius:50%; animation:spin-loader 1s linear infinite;"></div>
          </div>
        </div>
      </div>
    `;
    
    // Inject loader keyframes style if not exist
    if (!document.getElementById('spinner-loader-style')) {
      const style = document.createElement('style');
      style.id = 'spinner-loader-style';
      style.innerHTML = `
        @keyframes spin-loader {
          to { transform: rotate(360deg); }
        }
      `;
      document.head.appendChild(style);
    }
    
    document.body.appendChild(modal);
  }
  
  modal.classList.add('show');
  document.body.style.overflow = 'hidden';
  
  document.getElementById('vm-detail-modal-body').innerHTML = `
    <div style="display:flex; justify-content:center; padding: 40px 0;">
      <div class="spinner-loader" style="width:30px; height:30px; border:3px solid #E2E8F0; border-top-color:#1B4B43; border-radius:50%; animation:spin-loader 1s linear infinite;"></div>
    </div>
  `;

  fetch(`/api/vms/${name}/detail`)
    .then(r => {
      if (!r.ok) return r.text().then(t => { throw new Error(t); });
      return r.text();
    })
    .then(html => {
      document.getElementById('vm-detail-modal-body').innerHTML = html;
      document.getElementById('vm-detail-title').innerText = `${name} - Sistem Detayları`;
    })
    .catch(err => {
      document.getElementById('vm-detail-modal-body').innerText = 'Hata: ' + err.message;
    });
}

function closeVMDetailModal() {
  const modal = document.getElementById('vm-detail-modal');
  if (modal) modal.classList.remove('show');
  document.body.style.overflow = '';
}

// Create Segment Modal Control
function openSegmentModal() {
  const modal = document.getElementById('segment-modal');
  if (modal) {
    modal.classList.add('show');
    document.body.style.overflow = 'hidden';
    document.getElementById('segment-name-input').value = '';
    document.getElementById('segment-form-response').innerHTML = '';
    setTimeout(() => {
      document.getElementById('segment-name-input').focus();
    }, 150);
  }
}

function closeSegmentModal() {
  const modal = document.getElementById('segment-modal');
  if (modal) {
    modal.classList.remove('show');
    document.body.style.overflow = '';
  }
}

function handleSegmentSubmit(event) {
  // HTMX handles POST via hx-post. We just monitor for successful callback or close events
}

// VM Segment Assignment Modal Control
let activeNetworkVM = null;

function openNetworkModal(vmName) {
  activeNetworkVM = vmName;
  const nameEl = document.getElementById('network-vm-name');
  if (nameEl) nameEl.innerText = vmName;
  
  const selectEl = document.getElementById('network-segment-select');
  if (selectEl) {
    selectEl.innerHTML = '<option value="">Yükleniyor...</option>';
    
    // Fetch segments list from backend
    fetch('/api/network/segments')
      .then(resp => {
        if (!resp.ok) throw new Error("Ağ segmentleri yüklenemedi");
        return resp.json();
      })
      .then(segments => {
        selectEl.innerHTML = '<option value="">-- Seçiniz (Segment Yok) --</option>';
        segments.forEach(seg => {
          const opt = document.createElement('option');
          opt.value = seg.name;
          opt.innerText = `${seg.name} (VLAN ${seg.vlan_id})`;
          
          // Preselect current segment if the VM is assigned
          if (seg.vms && seg.vms.includes(vmName)) {
            opt.selected = true;
          }
          selectEl.appendChild(opt);
        });
      })
      .catch(err => {
        selectEl.innerHTML = `<option value="">Hata: ${err.message}</option>`;
      });
  }
  
  const modal = document.getElementById('network-modal');
  if (modal) modal.classList.add('show');
  document.body.style.overflow = 'hidden';
}

function closeNetworkModal() {
  activeNetworkVM = null;
  const modal = document.getElementById('network-modal');
  if (modal) modal.classList.remove('show');
  document.body.style.overflow = '';
}

function submitChangeNetwork(event) {
  event.preventDefault();
  if (!activeNetworkVM) return;
  
  const selectEl = document.getElementById('network-segment-select');
  const segmentName = selectEl.value;
  
  if (!segmentName) {
    alert("Lütfen geçerli bir segment seçin.");
    return;
  }
  
  fetch(`/api/network/segments/${encodeURIComponent(segmentName)}/assign`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json'
    },
    body: JSON.stringify({ vm: activeNetworkVM })
  })
  .then(resp => {
    if (!resp.ok) {
      return resp.text().then(txt => { throw new Error(txt); });
    }
    closeNetworkModal();
    // Refresh current layout/dashboard if active
    refreshCurrentTab();
  })
  .catch(err => {
    alert("Segment değiştirme hatası: " + err.message);
  });
}

// Listen to HTMX events indicating segments table has updated
document.body.addEventListener('segments-updated', () => {
  closeSegmentModal();
  closeNetworkModal();
  
  // Refresh layout/dashboard automatically if on VMs page
  const content = document.getElementById('dashboard-content');
  if (content) {
    refreshCurrentTab();
  }
});

function refreshCurrentTab() {
  const dashboard = document.getElementById('dashboard-content');
  if (!dashboard) return;

  const activeMenu = document.querySelector('.menu-item.active');
  if (!activeMenu) {
    htmx.trigger('#dashboard-content', 'load');
    return;
  }

  const id = activeMenu.id;
  if (id === 'menu-dashboard' || id === 'menu-vms') {
    htmx.ajax('GET', '/api/vms', '#dashboard-content');
  } else if (id === 'menu-network') {
    htmx.ajax('GET', '/api/network/segments', '#dashboard-content');
  } else if (id === 'menu-security') {
    htmx.ajax('GET', '/api/security/page', '#dashboard-content');
  } else if (id === 'menu-backup') {
    htmx.ajax('GET', '/api/backup/page', '#dashboard-content');
  } else if (id === 'menu-settings') {
    htmx.ajax('GET', '/api/settings/page', '#dashboard-content');
  } else {
    htmx.trigger('#dashboard-content', 'load');
  }
}
