class ChipSelect {
    constructor(container, endpoint, name) {
        this.container = container;
        this.endpoint = endpoint;
        this.name = name;
        this.selected = [];
        this.debounceTimer = null;

        this.chips = container.querySelector('.chips');
        this.input = container.querySelector('input[type="text"]');
        this.dropdown = container.querySelector('.dropdown');
        this.hiddenInput = container.querySelector('input[type="hidden"]');

        this.input.addEventListener('input', () => this.onInput());
        this.input.addEventListener('keydown', (e) => this.onKeydown(e));

        // Close dropdown when clicking outside
        document.addEventListener('click', (e) => {
            if (!this.container.contains(e.target)) {
                this.dropdown.classList.add('hidden');
            }
        });
    }

    onInput() {
        clearTimeout(this.debounceTimer);
        this.debounceTimer = setTimeout(() => this.fetchSuggestions(), 200);
    }

    onKeydown(e) {
        if (e.key === 'Backspace' && this.input.value === '' && this.selected.length > 0) {
            this.removeChip(this.selected.length - 1);
        }
    }

    async fetchSuggestions() {
        const q = this.input.value.trim();
        if (q.length === 0) {
            this.dropdown.classList.add('hidden');
            return;
        }

        try {
            const resp = await fetch(this.endpoint + '?q=' + encodeURIComponent(q));
            const items = await resp.json();

            // Filter out already selected
            const filtered = items.filter(item => !this.selected.includes(item));

            if (filtered.length === 0) {
                this.dropdown.classList.add('hidden');
                return;
            }

            this.dropdown.innerHTML = filtered.map(item =>
                '<div class="px-3 py-2 text-sm text-slate-700 hover:bg-blue-50 cursor-pointer">' +
                    this.escapeHtml(item) +
                '</div>'
            ).join('');

            this.dropdown.querySelectorAll('div').forEach(div => {
                div.addEventListener('click', () => {
                    this.addChip(div.textContent);
                });
            });

            this.dropdown.classList.remove('hidden');
        } catch (err) {
            console.error('ChipSelect fetch error:', err);
            this.dropdown.classList.add('hidden');
        }
    }

    addChip(value) {
        if (!this.selected.includes(value)) {
            this.selected.push(value);
            this.render();
        }
        this.input.value = '';
        this.dropdown.classList.add('hidden');
        this.input.focus();
    }

    removeChip(index) {
        this.selected.splice(index, 1);
        this.render();
    }

    render() {
        this.chips.innerHTML = this.selected.map((val, i) =>
            '<span class="inline-flex items-center gap-1 bg-blue-100 text-blue-800 text-xs font-medium px-2.5 py-1 rounded-full">' +
                this.escapeHtml(val) +
                '<button type="button" data-index="' + i + '" class="ml-0.5 text-blue-600 hover:text-blue-900 cursor-pointer bg-transparent border-none text-xs leading-none">&times;</button>' +
            '</span>'
        ).join('');

        this.chips.querySelectorAll('button').forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                this.removeChip(parseInt(btn.dataset.index));
            });
        });

        this.hiddenInput.value = this.selected.join(',');
    }

    getValues() {
        return this.selected;
    }

    escapeHtml(str) {
        const div = document.createElement('div');
        div.textContent = str;
        return div.innerHTML;
    }
}
