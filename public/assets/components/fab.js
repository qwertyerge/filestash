import { loadCSS } from "../helpers/loader.js";
import assert from "../lib/assert.js";

await loadCSS(import.meta.url, "./fab.css");

export default class ComponentFab extends HTMLButtonElement {
    constructor() {
        super();
        this.innerHTML = `<div class="content"></div>`;
        this.classList.add("component_fab");
    }

    async render($icon) {
        assert.type(this.querySelector(".content"), HTMLElement).replaceChildren($icon);
    }
}

customElements.define("component-fab", ComponentFab, { extends: "button" });
