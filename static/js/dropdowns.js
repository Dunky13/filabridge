// FilaBridge Dashboard - Dropdown Functionality

let previousSpoolLocationModalState = null;

function escapeHtml(value) {
    const div = document.createElement('div');
    div.textContent = value == null ? '' : String(value);
    return div.innerHTML;
}

function buildDropdownButtonMarkup(selectedText, selectedColor, arrow = '▼') {
    const swatchColor = selectedColor ? `#${selectedColor}` : '#ccc';
    return `
        <div style="display: flex; align-items: center; gap: 10px;">
            <div class="color-swatch" style="background-color: ${swatchColor};"></div>
            <span>${escapeHtml(selectedText)}</span>
        </div>
        <span class="dropdown-arrow">${arrow}</span>
    `;
}

function getDropdownState(dropdown) {
    const button = dropdown.querySelector('.dropdown-button');
    const hiddenInput = dropdown.querySelector('input[type="hidden"]');

    return {
        button,
        hiddenInput,
        value: hiddenInput ? hiddenInput.value : '',
        buttonHTML: button ? button.innerHTML : ''
    };
}

function setDropdownSelectedOption(dropdown, selectedValue) {
    dropdown.querySelectorAll('.dropdown-option').forEach(option => {
        option.classList.toggle('selected', (option.dataset.value || '') === selectedValue);
    });
}

function getDropdownOption(dropdown, selectedValue) {
    return Array.from(dropdown.querySelectorAll('.dropdown-option')).find(option => (option.dataset.value || '') === selectedValue);
}

function getDropdownOptionText(dropdown, selectedValue) {
    const option = getDropdownOption(dropdown, selectedValue);
    const optionText = option ? option.querySelector('.option-text') : null;
    return optionText ? optionText.textContent : '';
}

function applyDropdownSelection(dropdown, selectedValue, selectedText, selectedColor) {
    const state = getDropdownState(dropdown);

    if (state.hiddenInput) {
        state.hiddenInput.value = selectedValue;
    }

    if (state.button) {
        state.button.innerHTML = buildDropdownButtonMarkup(selectedText, selectedColor);
    }

    setDropdownSelectedOption(dropdown, selectedValue);
}

function closeDropdown(dropdown) {
    const content = dropdown.querySelector('.dropdown-content');
    const button = dropdown.querySelector('.dropdown-button');
    const arrow = dropdown.querySelector('.dropdown-arrow');

    if (content) {
        content.classList.remove('show');
    }
    if (button) {
        button.classList.remove('open');
    }
    if (arrow) {
        arrow.classList.remove('open');
    }
}

async function fetchJsonOrThrow(url, options) {
    const response = await fetch(url, options);
    let data;

    try {
        data = await response.json();
    } catch (error) {
        throw new Error(`Unexpected response from ${url}`);
    }

    if (!response.ok || data.error) {
        throw new Error(data.error || `Request failed for ${url}`);
    }

    return data;
}

async function getStorageLocations() {
    const data = await fetchJsonOrThrow('/api/locations');
    return (data.locations || []).filter(location => !location.is_virtual && location.type !== 'printer');
}

async function getAutoAssignPreviousSpoolSettings() {
    return fetchJsonOrThrow('/api/config/auto-assign-previous-spool');
}

function showPreviousSpoolLocationModal(spoolText, locations) {
    const modal = document.getElementById('previousSpoolLocationModal');
    const message = document.getElementById('previousSpoolLocationMessage');
    const select = document.getElementById('previousSpoolLocationSelect');

    if (!modal || !message || !select) {
        return Promise.reject(new Error('Previous spool modal is not available'));
    }

    message.textContent = `${spoolText} is leaving toolhead. Where should it be stored?`;
    select.innerHTML = '<option value="">Select a location...</option>';

    locations
        .slice()
        .sort((a, b) => (a.name || '').localeCompare(b.name || '', undefined, {sensitivity: 'base'}))
        .forEach(location => {
            const option = document.createElement('option');
            option.value = location.name;
            option.textContent = location.name;
            select.appendChild(option);
        });

    if (locations.length === 1) {
        select.value = locations[0].name;
    }

    modal.style.display = 'block';
    select.focus();

    return new Promise(resolve => {
        previousSpoolLocationModalState = {resolve};
    });
}

function closePreviousSpoolLocationModal(confirmed) {
    const modal = document.getElementById('previousSpoolLocationModal');
    const select = document.getElementById('previousSpoolLocationSelect');

    if (!modal || !previousSpoolLocationModalState) {
        return;
    }

    const selectedLocation = confirmed && select ? select.value.trim() : null;
    const {resolve} = previousSpoolLocationModalState;
    previousSpoolLocationModalState = null;

    modal.style.display = 'none';
    resolve(selectedLocation);
}

function confirmPreviousSpoolLocationModal() {
    const select = document.getElementById('previousSpoolLocationSelect');
    if (!select || !select.value.trim()) {
        alert('Select location for previous spool.');
        return;
    }

    closePreviousSpoolLocationModal(true);
}

function initPreviousSpoolLocationModal() {
    const modal = document.getElementById('previousSpoolLocationModal');
    if (!modal || modal.dataset.initialized === 'true') {
        return;
    }

    modal.dataset.initialized = 'true';
    modal.addEventListener('click', (event) => {
        if (event.target === modal) {
            closePreviousSpoolLocationModal(false);
        }
    });
}

async function resolvePreviousSpoolLocation(dropdown, previousValue, nextValue) {
    if (!previousValue || previousValue === nextValue) {
        return '';
    }

    const [settings, storageLocations] = await Promise.all([
        getAutoAssignPreviousSpoolSettings(),
        getStorageLocations()
    ]);

    const defaultLocation = (settings.location || '').trim();
    const hasValidDefault = settings.enabled && defaultLocation && storageLocations.some(location => location.name === defaultLocation);
    if (hasValidDefault) {
        return defaultLocation;
    }

    if (storageLocations.length === 0) {
        throw new Error('No storage locations available. Add one in Settings first.');
    }

    const previousSpoolText = getDropdownOptionText(dropdown, previousValue) || `Spool ${previousValue}`;
    return showPreviousSpoolLocationModal(previousSpoolText, storageLocations);
}

async function handleDropdownOptionSelection(dropdown, option) {
    const selectedText = option.querySelector('.option-text').textContent;
    const selectedColor = option.dataset.color || '';
    const selectedValue = option.dataset.value || '';
    const previousState = getDropdownState(dropdown);

    closeDropdown(dropdown);

    if (selectedValue === previousState.value) {
        return;
    }

    let previousSpoolLocation = '';
    try {
        previousSpoolLocation = await resolvePreviousSpoolLocation(dropdown, previousState.value, selectedValue);
    } catch (error) {
        alert(error.message);
        return;
    }

    if (previousSpoolLocation === null) {
        return;
    }

    await autoMapSpool(dropdown, {
        selectedValue,
        selectedText,
        selectedColor,
        previousState,
        previousSpoolLocation
    });
}

function attachDropdownOptionHandlers(dropdown, optionsContainer) {
    optionsContainer.querySelectorAll('.dropdown-option').forEach(option => {
        option.onclick = async (event) => {
            event.stopPropagation();
            await handleDropdownOptionSelection(dropdown, option);
        };
    });
}

// Load available spools for a specific dropdown
async function loadAvailableSpools(dropdown) {
    const toolheadRow = dropdown.closest('.toolhead-mapping-row');
    if (!toolheadRow) return;

    const printerId = toolheadRow.getAttribute('data-printer-id');
    const toolheadId = toolheadRow.getAttribute('data-toolhead-id');

    // Find printer name from printer element
    const printerElement = document.querySelector(`[data-printer-id="${printerId}"]`);
    if (!printerElement) return;

    const printerNameElement = printerElement.querySelector('h3');
    if (!printerNameElement) return;

    const printerName = printerNameElement.textContent;

    try {
        const response = await fetch(`/api/available_spools?printer_name=${encodeURIComponent(printerName)}&toolhead_id=${toolheadId}`);
        const data = await response.json();

        if (data.error) {
            console.error('Error loading available spools:', data.error);
            return;
        }

        const hiddenInput = dropdown.querySelector('input[type="hidden"]');
        const currentSpoolId = hiddenInput ? hiddenInput.value : '';
        const optionsContainer = dropdown.querySelector('.dropdown-options-container');
        if (!optionsContainer) return;

        // Preserve explicit empty option
        const emptyOption = optionsContainer.querySelector('.dropdown-option[data-value=""]');
        const noResults = optionsContainer.querySelector('.dropdown-no-results');
        optionsContainer.innerHTML = '';
        if (emptyOption) {
            emptyOption.classList.toggle('selected', currentSpoolId === '');
            optionsContainer.appendChild(emptyOption);
        }

        data.spools.forEach(spool => {
            const option = document.createElement('div');
            option.className = 'dropdown-option';
            option.setAttribute('data-value', spool.id);
            option.setAttribute('data-color', spool.filament?.color_hex || '');

            const colorSwatch = document.createElement('div');
            colorSwatch.className = 'color-swatch';
            colorSwatch.style.backgroundColor = `#${spool.filament?.color_hex || 'ccc'}`;

            const optionText = document.createElement('div');
            optionText.className = 'option-text';
            optionText.textContent = `[${spool.id}] ${spool.material || 'Unknown Material'} - ${spool.brand || 'Unknown Brand'} - ${spool.name || 'Unnamed Spool'}${spool.remaining_weight != null ? ` (${Math.round(spool.remaining_weight)}g remaining)` : ''}`;

            option.appendChild(colorSwatch);
            option.appendChild(optionText);

            if (currentSpoolId && spool.id.toString() === currentSpoolId) {
                option.classList.add('selected');
            }

            optionsContainer.appendChild(option);
        });

        if (noResults) {
            optionsContainer.appendChild(noResults);
        }

        attachDropdownOptionHandlers(dropdown, optionsContainer);
    } catch (error) {
        console.error('Error loading available spools:', error);
    }
}

// Custom dropdown functionality
function initCustomDropdowns() {
    initPreviousSpoolLocationModal();

    document.querySelectorAll('.custom-dropdown').forEach(dropdown => {
        // Skip NFC dropdowns - they have their own initialization
        if (dropdown.closest('#spool-tags-tab, #filament-tags-tab, #location-tags-tab')) {
            return;
        }

        const button = dropdown.querySelector('.dropdown-button');
        const content = dropdown.querySelector('.dropdown-content');
        const arrow = dropdown.querySelector('.dropdown-arrow');
        const searchInput = dropdown.querySelector('.dropdown-search');
        const optionsContainer = dropdown.querySelector('.dropdown-options-container');
        const noResults = dropdown.querySelector('.dropdown-no-results');

        if (!button || !content || !arrow || !optionsContainer) {
            return;
        }

        if (searchInput) {
            searchInput.addEventListener('input', (event) => {
                const searchTerm = event.target.value.toLowerCase().trim();
                const options = optionsContainer.querySelectorAll('.dropdown-option');
                let visibleCount = 0;

                options.forEach(option => {
                    const optionTextNode = option.querySelector('.option-text');
                    const optionText = optionTextNode ? optionTextNode.textContent.toLowerCase() : '';
                    let isMatch = searchTerm === '';

                    if (searchTerm !== '') {
                        if (/^\d+$/.test(searchTerm)) {
                            const idMatch = optionText.match(/^\[(\d+)\]/);
                            isMatch = idMatch && idMatch[1] === searchTerm;
                        } else {
                            const escapedSearch = searchTerm.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
                            const searchRegex = new RegExp(`\\b${escapedSearch}`, 'i');
                            isMatch = searchRegex.test(optionText);
                        }
                    }

                    option.style.display = isMatch ? 'flex' : 'none';
                    if (isMatch) {
                        visibleCount++;
                    }
                });

                if (noResults) {
                    noResults.style.display = visibleCount === 0 && searchTerm !== '' ? 'block' : 'none';
                }
            });
        }

        if (dropdown.dataset.initialized !== 'true') {
            attachDropdownOptionHandlers(dropdown, optionsContainer);

            button.addEventListener('click', async (event) => {
                event.stopPropagation();

                document.querySelectorAll('.custom-dropdown').forEach(otherDropdown => {
                    if (otherDropdown === dropdown) {
                        return;
                    }

                    closeDropdown(otherDropdown);

                    const otherSearch = otherDropdown.querySelector('.dropdown-search');
                    if (otherSearch) {
                        otherSearch.value = '';
                        otherSearch.dispatchEvent(new Event('input'));
                    }
                });

                const isOpening = !content.classList.contains('show');
                content.classList.toggle('show');
                button.classList.toggle('open');
                arrow.classList.toggle('open');

                if (isOpening) {
                    await loadAvailableSpools(dropdown);

                    if (searchInput) {
                        setTimeout(() => {
                            searchInput.focus();
                        }, 10);
                    }
                }
            });

            dropdown.dataset.initialized = 'true';
        }
    });

    if (document.body.dataset.dropdownCloseHandler !== 'true') {
        document.body.dataset.dropdownCloseHandler = 'true';
        document.addEventListener('click', () => {
            document.querySelectorAll('.custom-dropdown').forEach(dropdown => {
                closeDropdown(dropdown);

                const searchInput = dropdown.querySelector('.dropdown-search');
                if (searchInput) {
                    searchInput.value = '';
                    searchInput.dispatchEvent(new Event('input'));
                }
            });
        });
    }
}

// Auto-map spool to toolhead when selected
async function autoMapSpool(dropdown, {
    selectedValue,
    selectedText,
    selectedColor,
    previousState,
    previousSpoolLocation
}) {
    const toolheadRow = dropdown.closest('.toolhead-mapping-row');
    if (!toolheadRow) {
        console.error('Could not find toolhead mapping row');
        return;
    }

    const printerId = toolheadRow.getAttribute('data-printer-id');
    const toolheadId = toolheadRow.getAttribute('data-toolhead-id');

    const printerElement = document.querySelector(`[data-printer-id="${printerId}"]`);
    if (!printerElement) {
        console.error('Could not find printer element');
        return;
    }

    const printerNameElement = printerElement.querySelector('h3');
    if (!printerNameElement) {
        console.error('Could not find printer name element');
        return;
    }

    const printerName = printerNameElement.textContent;
    const button = dropdown.querySelector('.dropdown-button');
    if (!button) {
        return;
    }

    button.innerHTML = buildDropdownButtonMarkup(selectedText, selectedColor, '⏳');

    try {
        const response = await fetch('/api/map_toolhead', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({
                printer_name: printerName,
                toolhead_id: parseInt(toolheadId, 10),
                spool_id: selectedValue === '' ? 0 : parseInt(selectedValue, 10),
                previous_spool_location: previousSpoolLocation || ''
            })
        });

        const data = await response.json();
        if (!response.ok || data.error) {
            if (data.error && data.error.includes('is already assigned to')) {
                alert(`Spool assignment conflict: ${data.error}`);
            } else {
                alert(`Error mapping spool: ${data.error || response.statusText}`);
            }

            button.innerHTML = previousState.buttonHTML;
            return;
        }

        applyDropdownSelection(dropdown, selectedValue, selectedText, selectedColor);
        button.innerHTML = buildDropdownButtonMarkup(selectedText, selectedColor, '✅');
        updateEditButton(toolheadRow, selectedValue, selectedColor);

        setTimeout(() => {
            button.innerHTML = buildDropdownButtonMarkup(selectedText, selectedColor);
        }, 2000);

        if (selectedValue !== '') {
            removeSpoolFromOtherDropdowns(selectedValue);
        }
        refreshAllDropdowns();
    } catch (error) {
        console.error('Error mapping spool:', error);
        alert('Error mapping spool: ' + error.message);
        button.innerHTML = previousState.buttonHTML;
    }
}

// Immediately remove a spool from all other dropdowns
function removeSpoolFromOtherDropdowns(spoolId) {
    const allDropdowns = document.querySelectorAll('.custom-dropdown');

    allDropdowns.forEach(dropdown => {
        const optionsContainer = dropdown.querySelector('.dropdown-options-container');
        if (!optionsContainer) return;

        const optionToRemove = optionsContainer.querySelector(`[data-value="${spoolId}"]`);
        if (optionToRemove) {
            optionToRemove.remove();
        }
    });
}

// Refresh all dropdowns to update available spools
async function refreshAllDropdowns() {
    const allDropdowns = document.querySelectorAll('.custom-dropdown');

    for (const dropdown of allDropdowns) {
        const content = dropdown.querySelector('.dropdown-content');
        if (content && content.classList.contains('show')) {
            continue;
        }

        await loadAvailableSpools(dropdown);
    }
}

// Update edit button visibility and data based on selected spool
function updateEditButton(toolheadRow, selectedValue, selectedColor = '') {
    const editButton = toolheadRow.querySelector('.edit-spool-btn');
    if (!editButton) return;

    if (selectedValue && selectedValue !== '' && selectedValue !== '0') {
        editButton.classList.remove('hidden');
        editButton.setAttribute('data-spool-id', selectedValue);
        editButton.setAttribute('onclick', `openSpoolmanEdit(${selectedValue})`);

        if (selectedColor) {
            editButton.style.backgroundColor = '#' + selectedColor;
            editButton.style.borderColor = '#' + selectedColor;
        } else {
            editButton.style.backgroundColor = '#007bff';
            editButton.style.borderColor = '#007bff';
        }
    } else {
        editButton.classList.add('hidden');
        editButton.setAttribute('data-spool-id', '');
        editButton.setAttribute('onclick', 'openSpoolmanEdit(null)');
    }
}

// Open Spoolman edit page for a spool
function openSpoolmanEdit(spoolId) {
    if (!spoolId) {
        console.warn('No spool ID provided for editing');
        return;
    }

    const spoolmanBaseURL = document.body.dataset.spoolmanUrl;
    if (!spoolmanBaseURL) {
        alert('Spoolman URL not configured. Please check your settings.');
        return;
    }

    const editURL = `${spoolmanBaseURL}/spool/edit/${spoolId}`;
    window.open(editURL, '_blank');
}
