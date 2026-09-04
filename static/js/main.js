// FilaBridge Dashboard - Main JavaScript Functions

// Tab switching functionality
function switchTab(tabName, clickedElement) {
    // Hide all tab contents
    const tabContents = document.querySelectorAll('.tab-content');
    tabContents.forEach(content => {
        content.classList.remove('active');
    });
    
    // Remove active class from all tabs
    const tabs = document.querySelectorAll('.tab');
    tabs.forEach(tab => {
        tab.classList.remove('active');
    });
    
    // Show selected tab content
    document.getElementById(tabName + '-tab').classList.add('active');
    
    // Add active class to clicked tab
    if (clickedElement) {
        clickedElement.classList.add('active');
    } else {
        const tabButtons = document.querySelectorAll('.tab');
        tabButtons.forEach(btn => {
            if (btn.getAttribute('onclick') && btn.getAttribute('onclick').includes(`'${tabName}'`)) {
                btn.classList.add('active');
            }
        });
    }
    
    // Load configuration when settings tab is opened
    if (tabName === 'settings') {
        // Load data for the currently active settings sub-tab
        const activeSettingsTab = document.querySelector('.settings-tab.active');
        if (activeSettingsTab) {
            // Determine which tab is active and load its data
            const activeTabContent = document.querySelector('.settings-tab-content.active');
            if (activeTabContent) {
                const tabId = activeTabContent.id.replace('-tab', '');
                if (tabId === 'getting-started') {
                    // Getting Started tab doesn't need data loading
                } else if (tabId === 'basic-config') {
                    loadConfiguration();
                } else if (tabId === 'printers') {
                    loadPrinters();
                } else if (tabId === 'advanced') {
                    loadAdvancedSettings();
                    loadAutoAssignSettings();
                }
            }
        }
    } else if (tabName === 'history') {
        loadPrintHistory();
    }
}

function toggleConfig() {
    // Switch to the settings tab
    switchTab('settings');
}

// Settings sub-tab switching functionality
function switchSettingsTab(tabName, clickedElement) {
    // Hide all settings tab contents
    document.querySelectorAll('.settings-tab-content').forEach(tab => {
        tab.classList.remove('active');
    });
    
    // Remove active class from all settings tabs
    document.querySelectorAll('.settings-tab').forEach(tab => {
        tab.classList.remove('active');
    });
    
    // Show selected tab content
    const targetTab = document.getElementById(tabName + '-tab');
    if (targetTab) {
        targetTab.classList.add('active');
    }
    
    // Add active class to clicked tab
    if (clickedElement) {
        clickedElement.classList.add('active');
    } else {
        // Fallback: find the tab button by onclick attribute
        const tabButtons = document.querySelectorAll('.settings-tab');
        tabButtons.forEach(btn => {
            if (btn.getAttribute('onclick') && btn.getAttribute('onclick').includes(tabName)) {
                btn.classList.add('active');
            }
        });
    }
    
    // Load data for specific tabs
    if (tabName === 'getting-started') {
        // Getting Started tab doesn't need data loading
    } else if (tabName === 'basic-config') {
        loadConfiguration();
    } else if (tabName === 'printers') {
        loadPrinters();
    } else if (tabName === 'advanced') {
        loadAdvancedSettings();
        loadAutoAssignSettings();
    }
}

// Configuration Management
function loadConfiguration() {
    fetch('/api/config')
        .then(response => response.json())
        .then(config => {
            const form = document.getElementById('config-form');
            // Keep remote configuration values out of HTML parsing sinks. Build
            // static markup first, then assign data through DOM properties.
            form.innerHTML = `
                <div style="max-width: 600px; margin: 0 auto;">
                    <div class="form-group">
                        <label><strong>Spoolman URL:</strong></label>
                        <input type="text" id="spoolman_url" placeholder="http://localhost:8000">
                        <small>URL where Spoolman is running</small>
                    </div>
                    <div class="form-group">
                        <label><strong>Spoolman Username (optional):</strong></label>
                        <input type="text" id="spoolman_username" placeholder="Leave empty if not using basic auth">
                        <small>Username for Spoolman basic authentication (optional)</small>
                    </div>
                    <div class="form-group">
                        <label><strong>Spoolman Password (optional):</strong></label>
                        <input type="password" id="spoolman_password" autocomplete="new-password" placeholder="Leave blank to keep the stored password">
                        <small id="spoolman_password_status"></small>
                        <label><input type="checkbox" id="clear_spoolman_password"> Clear stored Spoolman password</label>
                    </div>
                    <div class="form-group">
                        <label><strong>Poll Interval (seconds):</strong></label>
                        <input type="number" id="poll_interval" min="10" max="300">
                        <small>How often to check printer status</small>
                    </div>
                    <div class="form-group">
                        <label><strong>Consumption Authority:</strong></label>
                        <select id="consumption_authority">
                            <option value="spoolman-led">Spoolman-led (automatic job debit)</option>
                            <option value="tag-led">OpenPrintTag-led (observe jobs only)</option>
                            <option value="observed-only">Observed only (no automatic debit)</option>
                        </select>
                        <small>Exactly one system may author consumption; this prevents double deductions.</small>
                    </div>
                    <div style="margin-top: 20px; text-align: center;">
                        <button class="btn" onclick="saveConfiguration()">💾 Save Configuration</button>
                    </div>
                </div>
            `;
            document.getElementById('spoolman_url').value = config.spoolman_url || '';
            document.getElementById('spoolman_username').value = config.spoolman_username || '';
            document.getElementById('poll_interval').value = config.poll_interval || '30';
            document.getElementById('consumption_authority').value = config.consumption_authority || 'spoolman-led';
            document.getElementById('spoolman_password_status').textContent = config.spoolman_password_configured
                ? 'A password is configured. Leave blank to keep it.'
                : 'No password is configured.';
        })
        .catch(error => {
            console.error('Error loading configuration:', error);
            document.getElementById('config-form').innerHTML = '<p style="color: red;">Error loading configuration</p>';
        });
}

function saveConfiguration() {
    const config = {
        spoolman_url: document.getElementById('spoolman_url').value,
        spoolman_username: document.getElementById('spoolman_username').value,
        spoolman_password: document.getElementById('spoolman_password').value,
        clear_spoolman_password: document.getElementById('clear_spoolman_password').checked,
        poll_interval: Number(document.getElementById('poll_interval').value),
        consumption_authority: document.getElementById('consumption_authority').value
    };
    
    fetch('/api/config', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(config)
    })
    .then(response => response.json())
    .then(data => {
        if (data.error) {
            alert('Error saving configuration: ' + data.error);
        } else {
            alert('Configuration saved successfully! The application will restart.');
            location.reload();
        }
    })
    .catch(error => {
        alert('Error saving configuration: ' + error.message);
    });
}

// Advanced Settings Functions
function loadAdvancedSettings() {
    fetch('/api/config')
        .then(response => response.json())
        .then(config => {
            document.getElementById('prusalinkTimeout').value = config.prusalink_timeout || '10';
            document.getElementById('prusalinkFileDownloadTimeout').value = config.prusalink_file_download_timeout || '60';
            document.getElementById('spoolmanTimeout').value = config.spoolman_timeout || '30';
        })
        .catch(error => {
            console.error('Error loading advanced settings:', error);
        });
}

function saveAdvancedSettings() {
    const config = {
        prusalink_timeout: Number(document.getElementById('prusalinkTimeout').value),
        prusalink_file_download_timeout: Number(document.getElementById('prusalinkFileDownloadTimeout').value),
        spoolman_timeout: Number(document.getElementById('spoolmanTimeout').value)
    };
    
    // Validate inputs
    if (config.prusalink_timeout < 5 || config.prusalink_timeout > 300) {
        alert('PrusaLink API timeout must be between 5 and 300 seconds');
        return;
    }
    if (config.prusalink_file_download_timeout < 10 || config.prusalink_file_download_timeout > 600) {
        alert('File download timeout must be between 10 and 600 seconds');
        return;
    }
    if (config.spoolman_timeout < 5 || config.spoolman_timeout > 300) {
        alert('Spoolman API timeout must be between 5 and 300 seconds');
        return;
    }
    
    fetch('/api/config', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(config)
    })
    .then(response => response.json())
    .then(data => {
        if (data.error) {
            alert('Error saving advanced settings: ' + data.error);
        } else {
            alert('Advanced settings saved successfully! The application will restart to apply changes.');
            location.reload();
        }
    })
    .catch(error => {
        alert('Error saving advanced settings: ' + error.message);
    });
}

function resetAdvancedSettings() {
    if (confirm('Reset all timeout settings to their default values?')) {
        document.getElementById('prusalinkTimeout').value = '10';
        document.getElementById('prusalinkFileDownloadTimeout').value = '60';
        document.getElementById('spoolmanTimeout').value = '30';
    }
}

// Auto-Assign Previous Spool Settings Functions
// Store the checkbox change handler so we can remove it before adding a new one
let autoAssignCheckboxHandler = null;

function loadAutoAssignSettings() {
    // First, load the settings
    fetch('/api/config/auto-assign-previous-spool')
        .then(response => response.json())
        .then(data => {
            if (data.error) {
                console.error('Error loading auto-assign settings:', data.error);
                return;
            }
            
            const enabled = data.enabled || false;
            const location = data.location || '';
            
            document.getElementById('autoAssignPreviousSpoolEnabled').checked = enabled;
            
            // Show/hide location dropdown based on checkbox
            const locationGroup = document.getElementById('autoAssignLocationGroup');
            if (locationGroup) {
                locationGroup.style.display = enabled ? 'block' : 'none';
            }
            
            // Load locations and populate dropdown
            return fetch('/api/locations')
                .then(response => response.json())
                .then(locationsData => {
                    if (locationsData.error) {
                        console.error('Error loading locations:', locationsData.error);
                        return;
                    }
                    
                    const locationSelect = document.getElementById('autoAssignPreviousSpoolLocation');
                    if (!locationSelect) return;
                    
                    // Clear existing options except the first one
                    locationSelect.innerHTML = '<option value="">Select a location...</option>';
                    
                    // Filter out printer toolhead locations (we only want storage locations)
                    const storageLocations = locationsData.locations.filter(loc => {
                        return !loc.is_virtual && loc.type !== 'printer';
                    });
                    
                    // Sort locations alphabetically by name
                    storageLocations.sort((a, b) => {
                        const nameA = (a.name || '').toLowerCase();
                        const nameB = (b.name || '').toLowerCase();
                        return nameA.localeCompare(nameB);
                    });
                    
                    // Add locations to dropdown
                    storageLocations.forEach(loc => {
                        const option = document.createElement('option');
                        option.value = loc.name;
                        option.textContent = loc.name;
                        if (loc.name === location) {
                            option.selected = true;
                        }
                        locationSelect.appendChild(option);
                    });
                    
                    // If the saved location is not in the list (e.g., it was deleted), add it as selected
                    if (location && !storageLocations.find(loc => loc.name === location)) {
                        const option = document.createElement('option');
                        option.value = location;
                        option.textContent = location + ' (not found)';
                        option.selected = true;
                        locationSelect.appendChild(option);
                    }
                })
                .catch(error => {
                    console.error('Error loading locations:', error);
                });
        })
        .then(() => {
            // Set up checkbox change handler
            const checkbox = document.getElementById('autoAssignPreviousSpoolEnabled');
            const locationGroup = document.getElementById('autoAssignLocationGroup');
            
            if (checkbox && locationGroup) {
                // Remove existing event listener if it exists
                if (autoAssignCheckboxHandler) {
                    checkbox.removeEventListener('change', autoAssignCheckboxHandler);
                }
                
                // Create and store the new handler function
                autoAssignCheckboxHandler = function() {
                    locationGroup.style.display = this.checked ? 'block' : 'none';
                };
                
                // Add the event listener
                checkbox.addEventListener('change', autoAssignCheckboxHandler);
            }
        })
        .catch(error => {
            console.error('Error loading auto-assign settings:', error);
        });
}

function saveAutoAssignSettings() {
    const enabled = document.getElementById('autoAssignPreviousSpoolEnabled').checked;
    const locationSelect = document.getElementById('autoAssignPreviousSpoolLocation');
    const location = locationSelect ? locationSelect.value.trim() : '';
    
    const settings = {
        enabled: enabled,
        location: location
    };
    
    fetch('/api/config/auto-assign-previous-spool', {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(settings)
    })
    .then(response => response.json())
    .then(data => {
        if (data.error) {
            alert('Error saving auto-assign settings: ' + data.error);
        } else {
            alert('Auto-assign settings saved successfully!');
        }
    })
    .catch(error => {
        alert('Error saving auto-assign settings: ' + error.message);
    });
}

// Utility Functions
function apiUrl(path) {
    // Ensure path starts with / if not already
    if (!path.startsWith('/')) {
        path = '/' + path;
    }
    return `${window.location.origin}${path}`;
}

// Initialize color swatches based on data-color attributes
function initColorSwatches() {
    document.querySelectorAll('.color-swatch[data-color]').forEach(swatch => {
        const color = swatch.getAttribute('data-color');
        if (color) {
            swatch.style.backgroundColor = '#' + color;
        }
    });
}

// Initialize edit button colors from data attributes
function initEditButtonColors() {
    document.querySelectorAll('.edit-spool-btn[data-color-hex]').forEach(button => {
        const colorHex = button.getAttribute('data-color-hex');
        if (colorHex) {
            button.style.backgroundColor = '#' + colorHex;
            button.style.borderColor = '#' + colorHex;
        }
    });
}

// Convert server timestamps to local time
function convertTimestampsToLocal() {
    const timestampElements = document.querySelectorAll('.error-timestamp');
    timestampElements.forEach(element => {
        const timestampData = element.getAttribute('data-timestamp');
        if (timestampData) {
            const localTime = new Date(timestampData).toLocaleString();
            element.textContent = localTime;
        }
    });
}

// Initialize everything when page loads
document.addEventListener('DOMContentLoaded', function() {
    convertTimestampsToLocal();
    connectWebSocket();
    loadPrintHistory();
    loadNfcData();
    loadPrinters();
    initCustomDropdowns();
    initColorSwatches();
    initEditButtonColors();
});
