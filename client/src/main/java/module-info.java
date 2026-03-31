module com.philstar.stargate {
    requires javafx.controls;
    requires javafx.fxml;

    // Add the standard JDK desktop module to access java.awt
    requires java.desktop;

    requires io.grpc;
    requires tomcat.annotations.api;

    opens com.philstar.stargate to javafx.fxml;
    opens com.philstar.stargate.controllers to javafx.fxml;
    exports com.philstar.stargate;
}