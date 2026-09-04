// FilaBridge Dashboard - Printer Management Functions

const printerPresetsById = new Map();
let printerPresetsPromise;

function loadPrinterPresets() {
    if (printerPresetsPromise) return printerPresetsPromise;

    printerPresetsPromise = fetch('/api/printer-presets')
        .then(response => {
            if (!response.ok) throw new Error(`HTTP ${response.status}`);
            return response.json();
        })
        .then(data => {
            printerPresetsById.clear();
            (data.presets || []).forEach(preset => printerPresetsById.set(preset.id, preset));
            populatePrinterPresetSelect('printerPreset', data.presets || [], data.custom_preset_id);
            populatePrinterPresetSelect('editPrinterPreset', data.presets || [], data.custom_preset_id);
            applyPrinterPresetSelection('add');
        })
        .catch(error => {
            printerPresetsPromise = undefined;
            console.error('Error loading printer presets:', error);
            throw error;
        });

    return printerPresetsPromise;
}

function populatePrinterPresetSelect(elementId, presets, customPresetId) {
    const select = document.getElementById(elementId);
    if (!select) return;

    const selected = select.value || customPresetId || 'custom';
    select.replaceChildren();
    const groups = new Map();
    presets.forEach(preset => {
        if (!groups.has(preset.group)) {
            const group = document.createElement('optgroup');
            group.label = preset.group;
            groups.set(preset.group, group);
            select.appendChild(group);
        }
        const option = document.createElement('option');
        option.value = preset.id;
        option.textContent = `${preset.name}${preset.preview ? ' (Preview)' : ''}`;
        groups.get(preset.group).appendChild(option);
    });

    const custom = document.createElement('option');
    custom.value = customPresetId || 'custom';
    custom.textContent = 'Custom / auto-detect';
    select.appendChild(custom);
    select.value = printerPresetsById.has(selected) ? selected : custom.value;
}

function applyPrinterPresetSelection(formName, customModel, customToolheads) {
    const edit = formName === 'edit';
    const presetSelect = document.getElementById(edit ? 'editPrinterPreset' : 'printerPreset');
    const modelInput = document.getElementById(edit ? 'editPrinterModel' : 'printerModel');
    const toolheadsInput = document.getElementById(edit ? 'editPrinterToolheads' : 'printerToolheads');
    if (!presetSelect || !modelInput || !toolheadsInput) return;

    const preset = printerPresetsById.get(presetSelect.value);
    const custom = !preset;
    modelInput.readOnly = !custom;
    toolheadsInput.readOnly = !custom;
    if (preset) {
        modelInput.value = preset.model;
        toolheadsInput.value = preset.toolheads;
    } else if (customModel !== undefined) {
        modelInput.value = customModel;
        toolheadsInput.value = customToolheads || 1;
    }
    if (edit && document.getElementById('editPrinterModal').style.display === 'block') {
        renderLogicalToolRoutes(parseInt(toolheadsInput.value), {});
    }
}

// Helper function to escape HTML attribute values to prevent XSS
function escapeHtmlAttribute(value) {
    if (value == null) return '';
    const div = document.createElement('div');
    div.textContent = value;
    return div.innerHTML.replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

// Printer Management Functions
function loadPrinters() {
    fetch('/api/printers')
        .then(response => response.json())
        .then(data => {
            const printerList = document.getElementById('printer-list');
            printerList.innerHTML = '';
            
            if (data.printers && Object.keys(data.printers).length > 0) {
                for (const [printerId, printer] of Object.entries(data.printers)) {
                    if (printerId === 'no_printers') continue;
                    
                    const printerCard = document.createElement('div');
                    printerCard.className = 'printer-card';
                    
                    // Build toolhead names section
                    let toolheadNamesHTML = '';
                    const toolheadNames = printer.toolhead_names || {};
                    for (let toolheadID = 0; toolheadID < (printer.toolheads || 1); toolheadID++) {
                        const currentName = toolheadNames[toolheadID] || `Toolhead ${toolheadID}`;
                        const escapedName = escapeHtmlAttribute(currentName);
                        toolheadNamesHTML += `
                            <div class="form-row" style="margin-bottom: 10px;">
                                <label style="min-width: 120px;">Toolhead ${toolheadID}:</label>
                                <input type="text" 
                                       id="toolhead-name-${printerId}-${toolheadID}" 
                                       value="${escapedName}" 
                                       class="toolhead-name-input"
                                       data-printer-id="${printerId}"
                                       data-toolhead-id="${toolheadID}"
                                       style="flex: 1; padding: 8px; border-radius: 4px; border: 1px solid #666; background: rgba(255,255,255,0.1); color: #fff;">
                            </div>
                        `;
                    }
                    
                    printerCard.innerHTML = `
                        <h3>${escapeHtmlAttribute(printer.name || 'Unknown Printer')}</h3>
                        <div class="printer-info">
                            <div><strong>Model:</strong> ${escapeHtmlAttribute(printer.model || 'Unknown')} (${printer.toolheads || 1} toolhead${printer.toolheads > 1 ? 's' : ''})</div>
                            <div><strong>Address:</strong> ${escapeHtmlAttribute(printer.ip_address || 'Not configured')}</div>
                            <div><strong>API Key:</strong> ${printer.api_key ? '••••••••' : 'Not configured'}</div>
                        </div>
                        <div class="printer-actions">
                            <button class="btn btn-small" onclick="editPrinter('${printerId}')">✏️ Edit</button>
                            <button class="btn btn-small" onclick="toggleToolheadNames('${printerId}')">🔤 Rename Toolheads</button>
                            <button class="btn btn-small btn-danger" onclick="deletePrinter('${printerId}')">🗑️ Delete</button>
                        </div>
                        <div id="toolhead-names-${printerId}" class="toolhead-names-section" style="display: none; margin-top: 15px; padding: 15px; background: rgba(255,255,255,0.05); border-radius: 5px;">
                            <h4 style="margin-top: 0; margin-bottom: 15px;">Toolhead Names</h4>
                            ${toolheadNamesHTML}
                            <div style="margin-top: 15px; text-align: right;">
                                <button class="btn btn-small" onclick="saveToolheadNames('${printerId}')">💾 Save Names</button>
                                <button class="btn btn-small btn-secondary" onclick="cancelToolheadNames('${printerId}')">❌ Cancel</button>
                            </div>
                        </div>
                    `;
                    printerList.appendChild(printerCard);
                }
            } else {
                printerList.innerHTML = '<div class="printer-card"><p>No printers configured. Click "Add Printer" to get started.</p></div>';
            }
        })
        .catch(error => {
            console.error('Error loading printers:', error);
            document.getElementById('printer-list').innerHTML = '<div class="printer-card"><p>Error loading printers. Please refresh the page.</p></div>';
        });
}

function showAddPrinterForm() {
    document.getElementById('addPrinterModal').style.display = 'block';
    document.getElementById('addPrinterForm').reset();
    applyPrinterPresetSelection('add', '', 1);
    
    // Reset button state AFTER form reset with a fresh query
    // Use setTimeout to ensure DOM is updated
    setTimeout(() => {
        const submitButton = document.querySelector('#addPrinterForm button[type="submit"]');
        if (submitButton) {
            submitButton.disabled = false;
            submitButton.textContent = 'Add Printer';
        }
    }, 0);
}

function closeAddPrinterModal() {
    document.getElementById('addPrinterModal').style.display = 'none';
    
    // Ensure button state is reset when modal is closed
    const submitButton = document.querySelector('#addPrinterForm button[type="submit"]');
    if (submitButton) {
        submitButton.disabled = false;
        submitButton.textContent = 'Add Printer';
    }
}

function closeEditPrinterModal() {
    document.getElementById('editPrinterModal').style.display = 'none';
    
    // Ensure button state is reset when modal is closed
    const submitButton = document.querySelector('#editPrinterForm button[type="submit"]');
    if (submitButton) {
        submitButton.disabled = false;
        submitButton.textContent = 'Update Printer';
    }
}

// Close modal when clicking outside of it
window.onclick = function(event) {
    const addModal = document.getElementById('addPrinterModal');
    const editModal = document.getElementById('editPrinterModal');
    if (event.target == addModal) {
        closeAddPrinterModal();
    } else if (event.target == editModal) {
        closeEditPrinterModal();
    }
}

function addPrinter(printerConfig) {
    return fetch('/api/printers', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(printerConfig)
    })
    .then(response => response.json())
    .then(data => {
        if (data.error) {
            throw new Error(data.error);
        }
        return data;
    });
}

// Handle form submission
document.getElementById('addPrinterForm').addEventListener('submit', function(e) {
    e.preventDefault();
    
    // Check if form is valid before proceeding
    if (!this.checkValidity()) {
        // Form has validation errors, don't change button state
        return;
    }
    
    const formData = new FormData(this);
    const printerConfig = {
        name: formData.get('name'),
        preset_id: formData.get('preset_id'),
        model: formData.get('model'),
        ip_address: formData.get('ip_address'),
        api_key: formData.get('api_key'),
        prusalink_username: formData.get('prusalink_username'),
        prusalink_password: formData.get('prusalink_password'),
        prusalink_custom_ca_pem: formData.get('prusalink_custom_ca_pem'),
        toolheads: parseInt(formData.get('toolheads'))
    };
    
    // Show loading state
    const submitButton = this.querySelector('button[type="submit"]');
    const originalText = submitButton.textContent;
    submitButton.disabled = true;
    submitButton.textContent = 'Detecting model...';
    
    // First detect printer model, then add printer
    detectModelAndAddPrinter(printerConfig, submitButton, originalText);
});

// Handle edit form submission
document.getElementById('editPrinterForm').addEventListener('submit', function(e) {
    e.preventDefault();
    
    const formData = new FormData(this);
    const printerId = formData.get('printerId');
    const name = formData.get('name');
    const presetId = formData.get('preset_id');
    const model = formData.get('model');
    const ipAddress = formData.get('ip_address');
    const apiKey = formData.get('api_key');
    const prusaLinkUsername = formData.get('prusalink_username');
    const prusaLinkPassword = formData.get('prusalink_password');
    const prusaLinkCustomCAPEM = formData.get('prusalink_custom_ca_pem');
    const toolheads = parseInt(formData.get('toolheads'));
    
    // Validate printerId is present
    if (!printerId) {
        alert('Error: Printer ID is missing. Please try again.');
        return;
    }
    
    // Show loading state
    const submitButton = this.querySelector('button[type="submit"]');
    if (!submitButton) {
        alert('Error: Submit button not found.');
        return;
    }
    
    const originalText = submitButton.textContent || 'Update Printer';
    submitButton.disabled = true;
    submitButton.textContent = 'Updating...';
    
    // Create printer config
    const printerConfig = {
        name: name,
        preset_id: presetId,
        model: model,
        ip_address: ipAddress,
        api_key: apiKey,
        prusalink_username: prusaLinkUsername,
        prusalink_password: prusaLinkPassword,
        prusalink_custom_ca_pem: prusaLinkCustomCAPEM,
        clear_api_key: formData.get('clear_api_key') === 'on',
        clear_prusalink_credentials: formData.get('clear_prusalink_credentials') === 'on',
        clear_prusalink_custom_ca_pem: formData.get('clear_prusalink_custom_ca_pem') === 'on',
        toolheads: toolheads
    };
    
    // Update the printer
    fetch(`/api/printers/${printerId}`, {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(printerConfig)
    })
    .then(response => response.json())
    .then(data => {
        if (data.error) {
            throw new Error(data.error);
        }
        
        return saveLogicalToolRoutes(printerId);
    })
    .then(() => {
        closeEditPrinterModal();
        loadPrinters();
    })
    .catch(error => {
        // Reset button state - ensure it always happens
        if (submitButton) {
            submitButton.disabled = false;
            submitButton.textContent = originalText;
        }
        alert('Error updating printer: ' + error.message);
    });
});

function detectModelAndAddPrinter(printerConfig, submitButton, originalText) {
    // Detect printer model only
    fetch('/api/detect_printer', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({
            ip_address: printerConfig.ip_address,
            api_key: printerConfig.api_key,
            prusalink_username: printerConfig.prusalink_username,
            prusalink_password: printerConfig.prusalink_password,
            prusalink_custom_ca_pem: printerConfig.prusalink_custom_ca_pem
        })
    })
    .then(response => response.json())
    .then(data => {
        // Check if there was an error (but still proceed if detection failed)
        if (data.error) {
            throw new Error(data.error);
        }
        
        // Show warning if detection failed but still proceed
        if (!data.detected && data.warning) {
            console.warn('Printer detection failed:', data.warning);
        }
        
        // Presets are authoritative. Detection only fills a custom blank model.
        if (printerConfig.preset_id === 'custom' && !printerConfig.model.trim()) {
            printerConfig.model = data.model || 'Unknown';
        }
        
        // Add the printer
        return addPrinter(printerConfig);
    })
    .then(() => {
        // Success - close modal and refresh
        closeAddPrinterModal();
        loadPrinters();
    })
    .catch(error => {
        // Reset button state
        submitButton.disabled = false;
        submitButton.textContent = originalText;
        alert('Error adding printer: ' + error.message);
    });
}

function editPrinter(printerId) {
    // Get the current printer data
    Promise.all([fetch('/api/printers').then(response => response.json()), loadPrinterPresets()])
        .then(([data]) => {
            const printer = data.printers[printerId];
            if (!printer) {
                alert('Printer not found');
                return;
            }
            
            // Populate the edit form with current data
            document.getElementById('editPrinterId').value = printerId;
            document.getElementById('editPrinterName').value = printer.name || '';
            document.getElementById('editPrinterIP').value = printer.ip_address || '';
            document.getElementById('editPrinterAPIKey').value = '';
            document.getElementById('editPrinterAPIKey').placeholder = printer.api_key_configured ? 'Stored — leave blank to keep' : 'Your PrusaLink API key';
            document.getElementById('editPrinterPrusaLinkUsername').value = printer.prusalink_username || '';
            document.getElementById('editPrinterPrusaLinkPassword').value = '';
            document.getElementById('editPrinterPrusaLinkPassword').placeholder = printer.prusalink_password_configured ? 'Stored — leave blank to keep' : '';
            document.getElementById('editPrinterPrusaLinkCA').value = '';
            document.getElementById('editPrinterPrusaLinkCA').placeholder = printer.prusalink_custom_ca_configured ? 'Stored — leave blank to keep' : '-----BEGIN CERTIFICATE-----';
            document.getElementById('editPrinterClearAPIKey').checked = false;
            document.getElementById('editPrinterClearDigest').checked = false;
            document.getElementById('editPrinterClearCA').checked = false;
            document.getElementById('editPrinterPreset').value = printer.preset_id || 'custom';
            applyPrinterPresetSelection('edit', printer.model || '', printer.toolheads || 1);
            return loadLogicalToolRoutes(printerId, printer.toolheads || 1);
        })
        .then(() => {
            
            // Show the edit modal
            document.getElementById('editPrinterModal').style.display = 'block';
        })
        .catch(error => {
            console.error('Error loading printer data:', error);
            alert('Error loading printer data');
        });
}

function loadLogicalToolRoutes(printerId, toolheads) {
    return fetch(`/api/printers/${printerId}/tool-routes`)
        .then(response => response.json())
        .then(data => {
            if (data.error) throw new Error(data.error);
            renderLogicalToolRoutes(toolheads, data.routes || {});
        });
}

function renderLogicalToolRoutes(toolheads, routes) {
    const container = document.getElementById('editPrinterToolRoutes');
    container.innerHTML = '';
    for (let logicalToolId = 0; logicalToolId < toolheads; logicalToolId++) {
        const row = document.createElement('label');
        row.textContent = `Slicer tool ${logicalToolId} → `;
        const select = document.createElement('select');
        select.dataset.logicalToolId = String(logicalToolId);
        for (let physicalToolheadId = 0; physicalToolheadId < toolheads; physicalToolheadId++) {
            const option = document.createElement('option');
            option.value = String(physicalToolheadId);
            option.textContent = `Physical input ${physicalToolheadId}`;
            select.appendChild(option);
        }
        select.value = String(routes[logicalToolId] ?? logicalToolId);
        row.appendChild(select);
        container.appendChild(row);
    }
}

function saveLogicalToolRoutes(printerId) {
    const selects = document.querySelectorAll('#editPrinterToolRoutes select[data-logical-tool-id]');
    return Promise.all(Array.from(selects, select => fetch(
        `/api/printers/${printerId}/tool-routes/${select.dataset.logicalToolId}`,
        {
            method: 'PUT',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({physical_toolhead_id: parseInt(select.value)})
        }
    ).then(response => {
        if (!response.ok) return response.json().then(data => { throw new Error(data.error || 'Failed to save tool route'); });
        return response.json();
    })));
}

document.addEventListener('DOMContentLoaded', function() {
    loadPrinterPresets().catch(() => {
        const addHelp = document.querySelector('#printerPreset + small');
        if (addHelp) addHelp.textContent = 'Preset catalog failed to load. Custom settings remain available.';
    });
});

function deletePrinter(printerId) {
    if (confirm('Are you sure you want to delete this printer?')) {
        fetch(`/api/printers/${printerId}`, {
            method: 'DELETE'
        })
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                alert('Error deleting printer: ' + data.error);
            } else {
                alert('Printer deleted successfully!');
                loadPrinters();
            }
        })
        .catch(error => {
            alert('Error deleting printer: ' + error.message);
        });
    }
}

// Toolhead Name Management Functions
function toggleToolheadNames(printerId) {
    const section = document.getElementById(`toolhead-names-${printerId}`);
    if (section.style.display === 'none') {
        section.style.display = 'block';
        // Store original values when opening
        const inputs = section.querySelectorAll('.toolhead-name-input');
        inputs.forEach(input => {
            input.dataset.originalValue = input.value;
        });
    } else {
        section.style.display = 'none';
    }
}

function saveToolheadNames(printerId) {
    const section = document.getElementById(`toolhead-names-${printerId}`);
    const inputs = section.querySelectorAll('.toolhead-name-input');
    const updates = [];
    
    // Collect all updates
    inputs.forEach(input => {
        const toolheadId = parseInt(input.dataset.toolheadId);
        const newName = input.value.trim();
        const originalName = input.dataset.originalValue || '';
        
        // Only update if name changed
        if (newName !== originalName && newName !== '') {
            updates.push({
                toolheadId: toolheadId,
                name: newName
            });
        }
    });
    
    if (updates.length === 0) {
        alert('No changes to save');
        return;
    }
    
    // Save each toolhead name
    const savePromises = updates.map(update => {
        return fetch(`/api/printers/${printerId}/toolheads/${update.toolheadId}`, {
            method: 'PUT',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({ name: update.name })
        })
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                throw new Error(data.error);
            }
            return data;
        });
    });
    
    // Execute all updates
    Promise.all(savePromises)
        .then(() => {
            alert('Toolhead names saved successfully!');
            // Close the section and reload printers to show updated names
            section.style.display = 'none';
            loadPrinters();
        })
        .catch(error => {
            alert('Error saving toolhead names: ' + error.message);
        });
}

function cancelToolheadNames(printerId) {
    const section = document.getElementById(`toolhead-names-${printerId}`);
    const inputs = section.querySelectorAll('.toolhead-name-input');
    
    // Restore original values
    inputs.forEach(input => {
        if (input.dataset.originalValue) {
            input.value = input.dataset.originalValue;
        }
    });
    
    section.style.display = 'none';
}
