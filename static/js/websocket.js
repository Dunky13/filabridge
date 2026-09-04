// FilaBridge Dashboard - WebSocket Functionality

// WebSocket client for real-time updates
let ws = null;
let reconnectAttempts = 0;
let maxReconnectAttempts = 10;
let reconnectDelay = 1000; // Start with 1 second

function connectWebSocket() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${protocol}//${window.location.host}/ws/status`;
    
    try {
        ws = new WebSocket(wsUrl);
        
        ws.onopen = function(event) {
            console.log('WebSocket connected');
            reconnectAttempts = 0;
            reconnectDelay = 1000;
            updateConnectionStatus('connected');
        };
        
        ws.onmessage = function(event) {
            try {
                const data = JSON.parse(event.data);
                if (data.type === 'status_update') {
                    updateDashboard(data);
                }
            } catch (error) {
                console.error('Error parsing WebSocket message:', error);
            }
        };
        
        ws.onclose = function(event) {
            console.log('WebSocket disconnected');
            updateConnectionStatus('disconnected');
            ws = null;
            
            // Attempt to reconnect with exponential backoff
            if (reconnectAttempts < maxReconnectAttempts) {
                setTimeout(() => {
                    reconnectAttempts++;
                    reconnectDelay = Math.min(reconnectDelay * 2, 30000); // Max 30 seconds
                    console.log(`Attempting to reconnect (${reconnectAttempts}/${maxReconnectAttempts}) in ${reconnectDelay}ms`);
                    connectWebSocket();
                }, reconnectDelay);
            } else {
                console.log('Max reconnection attempts reached');
                updateConnectionStatus('failed');
            }
        };
        
        ws.onerror = function(error) {
            console.error('WebSocket error:', error);
            updateConnectionStatus('error');
        };
        
    } catch (error) {
        console.error('Failed to create WebSocket connection:', error);
        updateConnectionStatus('error');
    }
}

function updateConnectionStatus(status) {
    // Find or create connection status indicator
    let statusIndicator = document.getElementById('ws-status');
    if (!statusIndicator) {
        statusIndicator = document.createElement('div');
        statusIndicator.id = 'ws-status';
        statusIndicator.style.cssText = `
            position: fixed;
            top: 10px;
            right: 10px;
            padding: 8px 12px;
            border-radius: 4px;
            font-size: 12px;
            font-weight: bold;
            z-index: 1000;
            transition: all 0.3s ease;
        `;
        document.body.appendChild(statusIndicator);
    }

    switch (status) {
        case 'connected':
            statusIndicator.textContent = '🟢 Live';
            statusIndicator.style.backgroundColor = '#28a745';
            statusIndicator.style.color = 'white';
            break;
        case 'disconnected':
            statusIndicator.textContent = '🟡 Connecting...';
            statusIndicator.style.backgroundColor = '#ffc107';
            statusIndicator.style.color = 'black';
            break;
        case 'error':
        case 'failed':
            statusIndicator.textContent = '🔴 Offline';
            statusIndicator.style.backgroundColor = '#dc3545';
            statusIndicator.style.color = 'white';
            break;
    }
}

function updateDashboard(data) {
    console.log('Updating dashboard with new data:', data);
    
    // Update printer statuses
    if (data.printers) {
        updatePrinterStatuses(data.printers);
    }
    
    // Update spool data
    if (data.spools) {
        updateSpoolData(data.spools);
    }
    
    // Update toolhead mappings
    if (data.toolhead_mappings) {
        updateToolheadMappings(data.toolhead_mappings);
    }
    
    // Update print errors
    if (data.print_errors) {
        updatePrintErrors(data.print_errors);
    }
}

function updatePrinterStatuses(printers) {
    Object.entries(printers).forEach(([printerId, printerData]) => {
        if (printerId === 'no_printers') return;
        
        // Find the printer element
        const printerElement = document.querySelector(`[data-printer-id="${printerId}"]`);
        if (!printerElement) return;
        
        // Update status badge
        const statusBadge = printerElement.querySelector('.status');
        if (statusBadge) {
            statusBadge.className = `status ${printerData.state}`;
            statusBadge.textContent = printerData.state;
        }

        const existingProgress = printerElement.querySelector('.printer-progress');
        const shouldShowProgress = printerData.state === 'PRINTING' || Boolean(printerData.current_job);

        if (!shouldShowProgress) {
            if (existingProgress) {
                existingProgress.remove();
            }
            return;
        }

        const progress = document.createElement('div');
        progress.className = 'printer-progress';
        const header = document.createElement('div');
        header.className = 'printer-progress-header';
        const titleGroup = document.createElement('div');
        const label = document.createElement('div');
        label.className = 'printer-progress-label';
        label.textContent = 'Current Print';
        const job = document.createElement('div');
        job.className = 'printer-progress-job';
        job.textContent = String(printerData.current_job || 'Active job');
        titleGroup.append(label, job);
        const percentage = Math.max(0, Math.min(100, Number(printerData.progress) || 0));
        const percent = document.createElement('div');
        percent.className = 'printer-progress-percent';
        percent.textContent = `${Math.round(percentage)}%`;
        header.append(titleGroup, percent);
        const progressBar = document.createElement('div');
        progressBar.className = 'printer-progress-bar';
        progressBar.setAttribute('aria-label', 'Print progress');
        const fill = document.createElement('div');
        fill.className = 'printer-progress-fill';
        fill.style.width = `${percentage}%`;
        progressBar.appendChild(fill);
        const meta = document.createElement('div');
        meta.className = 'printer-progress-meta';
        for (const text of [
            `Elapsed: ${formatDuration(printerData.print_time || 0)}`,
            `Remaining: ${formatDuration(printerData.print_time_left || 0)}`,
            `ETA: ${formatEta(printerData.print_time_left || 0)}`,
        ]) {
            const item = document.createElement('span');
            item.textContent = text;
            meta.appendChild(item);
        }
        progress.append(header, progressBar, meta);

        if (existingProgress) {
            existingProgress.replaceWith(progress);
        } else {
            const modelInfo = printerElement.querySelector('p');
            if (modelInfo) {
                modelInfo.insertAdjacentElement('afterend', progress);
            }
        }
    });
}

function formatDuration(totalSeconds) {
    if (!totalSeconds || totalSeconds <= 0) {
        return '0m';
    }

    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);

    if (hours > 0) {
        return `${hours}h ${String(minutes).padStart(2, '0')}m`;
    }

    return `${minutes}m`;
}

function formatEta(totalSeconds) {
    if (!totalSeconds || totalSeconds <= 0) {
        return '-';
    }

    const eta = new Date(Date.now() + totalSeconds * 1000);
    const now = new Date();

    const sameDay = eta.getFullYear() === now.getFullYear() &&
        eta.getMonth() === now.getMonth() &&
        eta.getDate() === now.getDate();

    if (sameDay) {
        return eta.toLocaleTimeString([], {hour: '2-digit', minute: '2-digit', hour12: false});
    }

    const tomorrow = new Date(now);
    tomorrow.setDate(now.getDate() + 1);
    const isTomorrow = eta.getFullYear() === tomorrow.getFullYear() &&
        eta.getMonth() === tomorrow.getMonth() &&
        eta.getDate() === tomorrow.getDate();

    if (isTomorrow) {
        return `Tomorrow ${eta.toLocaleTimeString([], {hour: '2-digit', minute: '2-digit', hour12: false})}`;
    }

    return eta.toLocaleString([], {month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false});
}

function normalizedWebsocketColor(value) {
    const color = String(value || '').replace(/^#/, '');
    return /^(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/.test(color)
        ? `#${color}`
        : '#ccc';
}

function renderSpoolDropdownButton(button, selectedText, selectedColor) {
    const value = document.createElement('div');
    value.style.cssText = 'display: flex; align-items: center; gap: 10px;';
    const swatch = document.createElement('div');
    swatch.className = 'color-swatch';
    swatch.style.backgroundColor = normalizedWebsocketColor(selectedColor);
    const text = document.createElement('span');
    text.textContent = String(selectedText || 'Select a spool...');
    value.append(swatch, text);
    const arrow = document.createElement('span');
    arrow.className = 'dropdown-arrow';
    arrow.textContent = '▼';
    button.replaceChildren(value, arrow);
}

function updateSpoolData(spools) {
    // Update spool dropdowns with new weight data
    document.querySelectorAll('.custom-dropdown').forEach(dropdown => {
        const optionsContainer = dropdown.querySelector('.dropdown-options-container');
        if (!optionsContainer) return;
        
        // Clear existing options except "Empty"
        const selectOption = optionsContainer.querySelector('.dropdown-option[data-value=""]');
        optionsContainer.replaceChildren();
        if (selectOption) {
            optionsContainer.appendChild(selectOption);
        }
        
        // Add updated spool options
        spools.forEach(spool => {
            const option = document.createElement('div');
            option.className = 'dropdown-option';
            option.setAttribute('data-value', spool.id);
            option.setAttribute('data-color', spool.filament?.color_hex || '');
            
            const colorSwatch = document.createElement('div');
            colorSwatch.className = 'color-swatch';
            colorSwatch.style.backgroundColor = '#' + (spool.filament?.color_hex || 'ccc');
            
            const optionText = document.createElement('div');
            optionText.className = 'option-text';
            optionText.textContent = `[${spool.id}] ${spool.material || 'Unknown Material'} - ${spool.brand || 'Unknown Brand'} - ${spool.name || 'Unnamed Spool'}${spool.remaining_weight != null ? ` (${Math.round(spool.remaining_weight)}g remaining)` : ''}`;
            
            option.appendChild(colorSwatch);
            option.appendChild(optionText);
            optionsContainer.appendChild(option);
        });
        
        // Add event listeners to the new options
        optionsContainer.querySelectorAll('.dropdown-option').forEach(option => {
            option.addEventListener('click', async function(e) {
                e.stopPropagation();
                
                // Update button text and selected state
                const selectedText = option.querySelector('.option-text').textContent;
                const selectedColor = option.dataset.color;
                const selectedValue = option.dataset.value;
                
                // Update hidden input value
                const hiddenInput = dropdown.querySelector('input[type="hidden"]');
                if (hiddenInput) {
                    hiddenInput.value = selectedValue;
                }
                
                // Update selected state
                optionsContainer.querySelectorAll('.dropdown-option').forEach(opt => opt.classList.remove('selected'));
                option.classList.add('selected');
                
                // Close dropdown
                const content = dropdown.querySelector('.dropdown-content');
                const button = dropdown.querySelector('.dropdown-button');
                const arrow = dropdown.querySelector('.dropdown-arrow');
                content.classList.remove('show');
                button.classList.remove('open');
                arrow.classList.remove('open');
                
                // Auto-map the spool if a spool is selected (not "Empty")
                if (selectedValue && selectedValue !== '') {
                    await autoMapSpool(dropdown, selectedValue, selectedText, selectedColor);
                } else {
                    // Handle empty selection - unmap the toolhead
                    await autoMapSpool(dropdown, '0', selectedText, '');
                }
                
                // Update edit button after selection
                const toolheadRow = dropdown.closest('.toolhead-mapping-row');
                if (toolheadRow) {
                    updateEditButton(toolheadRow, selectedValue, selectedColor);
                }
            });
        });
    });
}

function updateToolheadMappings(mappings) {
    // First, find all toolhead rows in the DOM
    const allToolheadRows = document.querySelectorAll('.toolhead-mapping-row');
    
    // Create a set of mapped toolheads for quick lookup
    const mappedToolheads = new Set();
    Object.entries(mappings).forEach(([printerId, printerMappings]) => {
        Object.entries(printerMappings).forEach(([toolheadId, mapping]) => {
            mappedToolheads.add(`${printerId}-${toolheadId}`);
        });
    });
    
    // Process all toolhead rows
    allToolheadRows.forEach(toolheadRow => {
        const printerId = toolheadRow.getAttribute('data-printer-id');
        const toolheadId = toolheadRow.getAttribute('data-toolhead-id');
        const key = `${printerId}-${toolheadId}`;
        
        // Find the dropdown
        const dropdown = toolheadRow.querySelector('.custom-dropdown');
        if (!dropdown) return;
        
        const hiddenInput = dropdown.querySelector('input[type="hidden"]');
        const dropdownButton = dropdown.querySelector('.dropdown-button');
        const optionsContainer = dropdown.querySelector('.dropdown-options-container');
        
        if (!dropdownButton) return;
        
        // Update toolhead label with display name if available
        const toolheadLabel = toolheadRow.querySelector('.toolhead-label');
        if (toolheadLabel && mappings[printerId] && mappings[printerId][toolheadId]) {
            const mapping = mappings[printerId][toolheadId];
            if (mapping.display_name) {
                toolheadLabel.textContent = mapping.display_name + ':';
            }
        }
        
        // Check if this toolhead has a mapping
        if (mappedToolheads.has(key) && mappings[printerId] && mappings[printerId][toolheadId]) {
            // Toolhead has a mapping - update it
            const mapping = mappings[printerId][toolheadId];
            const spoolId = mapping.spool_id;
            
            // Update hidden input
            if (hiddenInput) {
                hiddenInput.value = spoolId || '';
            }
            
            // Find the spool option
            if (optionsContainer && spoolId) {
                const spoolOption = optionsContainer.querySelector(`.dropdown-option[data-value="${spoolId}"]`);
                if (spoolOption) {
                    const selectedText = spoolOption.querySelector('.option-text').textContent;
                    const selectedColor = spoolOption.dataset.color;
                    
                    // Update button display
                    renderSpoolDropdownButton(dropdownButton, selectedText, selectedColor);
                    
                    // Mark as selected
                    optionsContainer.querySelectorAll('.dropdown-option').forEach(opt => {
                        opt.classList.remove('selected');
                    });
                    spoolOption.classList.add('selected');
                    
                    // Update edit button
                    updateEditButton(toolheadRow, spoolId, selectedColor);
                    
                    console.log(`Updated mapping for printer ${printerId}, toolhead ${toolheadId}: spool ${spoolId}`);
                }
            }
        } else {
            // Toolhead has NO mapping - clear it
            if (hiddenInput) {
                hiddenInput.value = '';
            }
            
            // Set to empty state
            renderSpoolDropdownButton(dropdownButton, 'Select a spool...', 'ccc');
            
            // Clear selected state
            if (optionsContainer) {
                optionsContainer.querySelectorAll('.dropdown-option').forEach(opt => {
                    opt.classList.remove('selected');
                });
            }
            
            // Update edit button for empty state
            updateEditButton(toolheadRow, '', '');
            
            console.log(`Cleared mapping for printer ${printerId}, toolhead ${toolheadId}`);
        }
    });
}

function updatePrintErrors(printErrors) {
    const container = document.getElementById('print-errors-container');
    if (!container) return;
    
    // Clear existing errors
    container.replaceChildren();
    
    if (printErrors.length === 0) {
        container.style.display = 'none';
        return;
    }
    
    container.style.display = 'block';
    
    // Add each error
    printErrors.forEach(error => {
        const errorElement = document.createElement('div');
        errorElement.className = 'print-error';
        errorElement.dataset.errorId = String(error.id || '');
        errorElement.style.cssText = 'background: #f8d7da; border: 1px solid #f5c6cb; color: #721c24; padding: 20px; margin: 20px 0; border-radius: 8px;';
        
        const timestamp = new Date(error.timestamp).toLocaleString();

        const heading = document.createElement('h4');
        heading.style.marginTop = '0';
        heading.textContent = '⚠️ Print Processing Failed';
        errorElement.appendChild(heading);

        const appendDetail = (label, value) => {
            const paragraph = document.createElement('p');
            const strong = document.createElement('strong');
            strong.textContent = label + ':';
            paragraph.append(strong, document.createTextNode(' ' + String(value ?? '')));
            errorElement.appendChild(paragraph);
        };
        appendDetail('Printer', error.printer_name);
        appendDetail('File', error.filename);
        appendDetail('Time', timestamp);
        appendDetail('Error', error.error);
        appendDetail('Action Required', 'Please update Spoolman manually with the correct filament usage for this print.');

        const acknowledgeButton = document.createElement('button');
        acknowledgeButton.className = 'btn';
        acknowledgeButton.style.cssText = 'background: #dc3545; margin-top: 10px;';
        acknowledgeButton.textContent = 'Acknowledge';
        acknowledgeButton.addEventListener('click', () => acknowledgeError(String(error.id || '')));
        errorElement.appendChild(acknowledgeButton);
        
        container.appendChild(errorElement);
    });
}

// Acknowledge print error
async function acknowledgeError(errorId) {
    try {
        const response = await fetch(`/api/print-errors/${encodeURIComponent(errorId)}/acknowledge`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
        });

        if (response.ok) {
            // Remove the error from the UI
            const errorElement = Array.from(document.querySelectorAll('[data-error-id]'))
                .find(element => element.dataset.errorId === String(errorId));
            if (errorElement) {
                errorElement.remove();
            }
            
            // Check if there are any remaining errors
            const remainingErrors = document.querySelectorAll('.print-error');
            if (remainingErrors.length === 0) {
                const container = document.getElementById('print-errors-container');
                if (container) {
                    container.style.display = 'none';
                }
            }
        } else {
            // Check if response is JSON
            const contentType = response.headers.get('content-type');
            if (contentType && contentType.includes('application/json')) {
                try {
                    const errorData = await response.json();
                    alert('Failed to acknowledge error: ' + (errorData.error || 'Unknown error'));
                } catch (jsonError) {
                    console.error('Failed to parse error response as JSON:', jsonError);
                    alert('Failed to acknowledge error: Invalid server response');
                }
            } else {
                // Response is not JSON, get text
                const errorText = await response.text();
                console.error('Non-JSON error response:', errorText);
                alert('Failed to acknowledge error: ' + (errorText || 'Unknown error'));
            }
        }
    } catch (error) {
        console.error('Error acknowledging print error:', error);
        alert('Failed to acknowledge error: ' + error.message);
    }
}
