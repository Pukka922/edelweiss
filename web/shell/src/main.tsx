import { render } from "solid-js/web";
import { App } from "./App";
import { applyTheme, loadTheme } from "./prefs";
import "./style.css";

applyTheme(loadTheme());
const root = document.getElementById("app");
if (root) render(() => <App />, root);
