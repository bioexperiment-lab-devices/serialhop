import { TitleBar } from "./components/TitleBar";
import { TabBar } from "./components/TabBar";
import { Footer } from "./components/Footer";
import { Lamp } from "./components/Lamp";

export function App() {
  return (
    <div>
      <TitleBar version="0.13.0" />
      <TabBar active="status" onChange={() => {}} />
      <Lamp name="Service" tone="green" label="Running" />
      <Footer kind="ok" text="Ready" time="15:04:23" />
    </div>
  );
}
