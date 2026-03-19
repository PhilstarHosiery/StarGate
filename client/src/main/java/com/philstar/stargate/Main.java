package com.philstar.stargate;

/**
 * Launcher entry point.
 *
 * This must be a plain class (NOT extending javafx.application.Application).
 * jlink/jpackage require the main class to be outside the JavaFX Application
 * lifecycle so the runtime can bootstrap the JavaFX platform correctly.
 */
public class Main {
    public static void main(String[] args) {
        StarGateApp.launch(StarGateApp.class, args);
    }
}
